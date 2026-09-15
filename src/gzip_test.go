// gzip compression regression test for /api/status.
//
// The panel polls every 3 seconds and repeated field names account for nearly 70%
// of this JSON. Compression is a pure transport-layer optimization: the frontend's
// fetch sends Accept-Encoding and decompresses automatically, so "decoded bytes must
// match the uncompressed response exactly" is the core assertion here - compression
// must not change any semantics.
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeGzipStatus builds a snapshot roughly the size of a real panel response: real
// long sessions draw hundreds of trend points plus 200 records and 200 message
// lines, making the whole JSON tens of KB.
func fakeGzipStatus() AppStatus {
	var st AppStatus
	st.LogFileName = "Journal.2026-09-11T142000.01.log"
	st.TotalKills, st.TotalBounty = 442, 41_300_000
	st.HourKills, st.HourBounty = 12, 1_440_000
	st.StatWindowText = "1 小时"

	for i := 0; i < 131; i++ {
		st.KillTrend = append(st.KillTrend, TrendPoint{
			TimeLocal: fmt.Sprintf("%02d:%02d", i/6%24, i%6*10),
			Kills:     1, Bounty: 120_000, KillsHour: 6,
		})
	}
	st.TrendWindowText = "21 小时 50 分"

	for i := 0; i < 200; i++ {
		st.BountyRecords = append(st.BountyRecords, BountyItem{
			TimeLocal: "12:30", Credits: 120_000, ShipType: "eagle",
		})
		st.MessageLines = append(st.MessageLines,
			"12:30 | 击杀 Pirate（Eagle）赏金 120,000 Cr")
	}
	st.UpdatedAt = "2026-09-11T16:50:00+08:00"
	return st
}

func snapshotGlobalStatus() AppStatus {
	statusLock.RLock()
	defer statusLock.RUnlock()
	return globalStatus
}

// getStatus issues one request and returns the raw body (client auto-decompression
// is disabled, otherwise compression would be unobservable).
func getStatus(url, acceptEncoding string) (*http.Response, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, url+"/api/status", nil)
	if err != nil {
		return nil, nil, err
	}
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	tr := &http.Transport{DisableCompression: true}
	defer tr.CloseIdleConnections()

	resp, err := tr.RoundTrip(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	return resp, body, err
}

// A gzip request must: get a truly compressed body, decode it, and decode
// byte-for-byte identically to the uncompressed response.
func TestStatusHandlerGzipRoundTrip(t *testing.T) {
	old := snapshotGlobalStatus()
	setStatus(fakeGzipStatus())
	defer setStatus(old)

	srv := httptest.NewServer(gzipHandler(http.HandlerFunc(statusHandler)))
	defer srv.Close()

	plainResp, plain, err := getStatus(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if ce := plainResp.Header.Get("Content-Encoding"); ce != "" {
		t.Fatalf("客户端没声明 gzip，不该压缩，却返回 Content-Encoding=%q", ce)
	}

	gzResp, gzRaw, err := getStatus(srv.URL, "gzip")
	if err != nil {
		t.Fatal(err)
	}
	if ce := gzResp.Header.Get("Content-Encoding"); ce != "gzip" {
		t.Fatalf("声明 gzip 后响应应被压缩，Content-Encoding=%q", ce)
	}
	if !strings.Contains(gzResp.Header.Get("Vary"), "Accept-Encoding") {
		t.Errorf("缺少 Vary: Accept-Encoding —— 中间缓存可能把压缩版发给不支持的客户端")
	}

	zr, err := gzip.NewReader(bytes.NewReader(gzRaw))
	if err != nil {
		t.Fatalf("响应体不是合法的 gzip 流：%v", err)
	}
	decoded, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("解压失败：%v", err)
	}
	if err := zr.Close(); err != nil {
		t.Fatalf("关闭 gzip 流失败：%v", err)
	}

	if !bytes.Equal(decoded, plain) {
		t.Errorf("解压结果与明文不一致：明文 %d B，解压后 %d B", len(plain), len(decoded))
	}

	var got AppStatus
	if err := json.Unmarshal(decoded, &got); err != nil {
		t.Fatalf("解压后不是合法 JSON：%v", err)
	}
	if got.TotalKills != 442 || len(got.KillTrend) != 131 || len(got.BountyRecords) != 200 {
		t.Errorf("字段在压缩往返中损坏：kills=%d 趋势点=%d 记录=%d",
			got.TotalKills, len(got.KillTrend), len(got.BountyRecords))
	}

	rate := float64(len(gzRaw)) / float64(len(plain)) * 100
	t.Logf("响应体 %d B → gzip %d B（%.1f%%）", len(plain), len(gzRaw), rate)
	if rate > 50 {
		t.Errorf("压缩率仅 %.1f%%，gzip 似乎没真正生效", rate)
	}
}

// gzip.Writer is reused from a sync.Pool: concurrent requests must each get their
// own correct stream with no cross-talk (the same class of risk as the earlier
// st.MessageLines shared-backing-array lesson).
func TestStatusHandlerGzipConcurrent(t *testing.T) {
	old := snapshotGlobalStatus()
	setStatus(fakeGzipStatus())
	defer setStatus(old)

	srv := httptest.NewServer(gzipHandler(http.HandlerFunc(statusHandler)))
	defer srv.Close()

	_, want, err := getStatus(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}

	const n = 8
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, raw, err := getStatus(srv.URL, "gzip")
			if err != nil {
				errCh <- err
				return
			}
			if resp.Header.Get("Content-Encoding") != "gzip" {
				errCh <- fmt.Errorf("并发请求中有的响应没压缩")
				return
			}
			zr, err := gzip.NewReader(bytes.NewReader(raw))
			if err != nil {
				errCh <- fmt.Errorf("并发响应不是合法 gzip 流：%w", err)
				return
			}
			decoded, err := io.ReadAll(zr)
			if err != nil {
				errCh <- fmt.Errorf("并发解压失败：%w", err)
				return
			}
			if !bytes.Equal(decoded, want) {
				errCh <- fmt.Errorf("并发响应解压后与预期不一致（可能是 Writer 复用时串了缓冲）")
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}
}

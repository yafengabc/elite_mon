# elite_mon 手机端（Capacitor）

把网页面板包成安卓 App。**本机不需要 Android SDK、Java 或 Gradle** —— 原生工程在
CI 里生成并编译，这里只放网页资产和 Capacitor 配置。

```
mobile/
├── www/                 网页资产（零构建，改完即生效）
│   ├── index.html       面板结构 + 服务器设置面板
│   ├── main.js          渲染逻辑（与桌面面板同源，只改了 API 基址）
│   ├── app.js           手机专属：服务器地址管理 / 连接状态
│   └── style.css        深色主题 + 移动端适配（安全区、窄屏单列）
├── capacitor.config.json
├── package.json         Capacitor 8（Node 22+，仅在 CI 与本地预览时需要）
└── .gitignore           node_modules/ 与 android/ 都不入库
```

## 桌面面板与它的关系

桌面面板是**同源**的：`fetch("/api/status")` 就是它自己。App 不行——它的页面来自手机，
origin 是 `http://localhost`，相对路径只会去问手机自己。所以 `app.js` 引入了一个
`apiBase()`：地址存在 localStorage 里，首次启动弹设置面板让你填电脑上那台
elite_mon 的地址（状态栏上显示的就是，形如 `http://192.168.1.5:8088`），填完点「保存并连接」，
它会**先试连一次**再保存，避免手滑打错就陷入无限「连接失败」。

这份代码是**独立副本**，不与 `src/static/` 共享，两边各自演进。

## 本地改前端

```sh
npm install          # 只装 Capacitor CLI，不碰安卓
```

然后直接编辑 `www/` 下的文件。想预览就用任意静态服务器（因为 fetch 的目标是你填的电脑地址，
不受端口限制）：

```sh
python -m http.server 8000 -d www    # 打开 http://localhost:8000
```

浏览器里同样会先弹设置面板，填电脑地址即可看到真实数据。

## 打包：交给 CI

`.github/workflows/android.yml`：

| 触发 | 结果 |
|---|---|
| Actions 里手动 **Run workflow** | 出 APK/AAB，作为 artifact 下载（保留 90 天） |
| 推 `v*` tag | 同上，并挂到该版本的 Release |
| 改动 `mobile/**` 的 PR | 只做一次完整构建校验 |

CI 做的是：`npm ci` → `npx cap add android` → 允许明文 → `cap sync` →
`gradlew assembleRelease bundleRelease` → 签名 → 上传。

## 两个安卓特有的坑（CI 已处理，改结构时别丢）

1. **明文流量**：Android 9+ 默认拒绝明文 HTTP，而我们要访问 `http://192.168.x.x:8088`。
   Capacitor 的模板 manifest 里**没有** `usesCleartextTraffic`，不补这个属性每个请求都会被
   系统拦掉、面板永远空白。CI 每次生成工程后都会打上这个补丁（manifest 每次重建，所以不能只改一次）。
2. **跨域**：App 的 origin 是 `http://localhost`（iOS 是 `capacitor://localhost`），
   请求电脑上的 `/api/status` 属于跨域。**服务端必须放行这两个 origin**，否则浏览器直接拦掉。
   见 elite_monitor.go 里的 CORS 处理。

## 签名（可选，上架 Play 才需要）

CI 在无密钥时也能成功，只是退化为**调试签名的 debug APK**（能装，但不能上架）。
要出签名包，一次性准备 keystore：

```sh
# 需要 JDK（只此一次；换台有 JDK 的机器生成也行）
keytool -genkey -v -keystore elite-mon.jks -alias elitemon \
        -keyalg RSA -keysize 2048 -validity 10000

base64 -w 0 elite-mon.jks > elite-mon.jks.b64      # Git Bash
```

把下面四个填进仓库 Settings → Secrets and variables → Actions：

| Secret | 值 |
|---|---|
| `ANDROID_KEYSTORE_BASE64` | `elite-mon.jks.b64` 的内容 |
| `ANDROID_KEYSTORE_PASSWORD` | keystore 密码 |
| `ANDROID_KEY_ALIAS` | `elitemon` |
| `ANDROID_KEY_PASSWORD` | 该别名的密码 |

⚠️ keystore 丢了就无法更新已上架的应用（Play 只认同一把钥匙）——请连同密码另存一份。
⚠️ `*.jks` 与 `*.b64` 不要提交进仓库。

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
`apiBase()`：地址存在 **Capacitor Preferences**（安卓侧落到原生 SharedPreferences，升级 App
不会丢；纯浏览器预览回退到 localStorage），在「设置」页填电脑上那台 elite_mon 的地址
（状态栏上显示的就是，只填 IP 即可，`192.168.1.5` 会自动补成 `http://192.168.1.5:8088`），
填完点「保存并连接」，它会**先试连一次**再保存，避免手滑打错就陷入无限「连接失败」；
连上过的地址会进连接历史，点一下即可回填重连。

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
| Actions 里手动 **Run workflow** | 出 `elite-mon-<git describe>.apk` + `.aab`，作为 artifact 下载（保留 90 天） |
| 推 `v*` tag | 同上命名，并挂到该版本的 Release |
| 改动 `mobile/**` 的 PR | 只做构建校验：PR 拿不到 secrets，不签名、不产出 |

CI 做的是：`npm ci` → `npx cap add android` → `cap sync` → 允许明文 →
`gradlew assembleRelease bundleRelease` → 签名（apksigner / jarsigner）→ 产出。

产物文件名取自 `git describe --tags --always`，与桌面版二进制里的版本串同一个口径：
提交正好被打过 tag 时就是 tag 名（`elite-mon-v1.0.11.apk`），否则是
`<tag>-<tag 之后的提交数>-g<短sha>`（`elite-mon-v1.0.11-1-g27b2f3b.apk`）。
不加 `dev` 之类的前缀——两种触发方式产出的都是同一份 release 签名构建，前缀只会让人
以为有个「非正式版」。checkout 因此需要 `fetch-depth: 0`（浅克隆没有 tag，`git describe`
会静默退回纯 sha）。

## 两个安卓特有的坑（CI 已处理，改结构时别丢）

1. **明文流量**：Android 9+ 默认拒绝明文 HTTP，而我们要访问 `http://192.168.x.x:8088`。
   Capacitor 的模板 manifest 里**没有** `usesCleartextTraffic`，不补这个属性每个请求都会被
   系统拦掉、面板永远空白。CI 每次生成工程后都会打上这个补丁（manifest 每次重建，所以不能只改一次）。
2. **跨域**：App 的 origin 是 `http://localhost`（iOS 是 `capacitor://localhost`），
   请求电脑上的 `/api/status` 属于跨域。**服务端必须放行这两个 origin**，否则浏览器直接拦掉。
   见 elite_monitor.go 里的 CORS 处理。

## 签名（必需）

CI **必须**拿到签名密钥，缺了就直接失败、不发布。原因不是洁癖：release APK 未签名时
**根本装不上**，而回退成 debug 版又是拿**公开的** Android debug key 签的（谁都能签出同名包，
证明不了来源），文件名 `app-debug.apk` 放在 Release 里也像是出错。

密钥库在 `keystore/`（整目录已 gitignore，**不入库**）：

| | |
|---|---|
| 文件 | `keystore/elite-mon.p12`（PKCS12，4096 位 RSA，有效期 100 年） |
| 别名 | `elitemon` |
| 密码 | `keystore/password.txt` |

仓库 Settings → Secrets and variables → Actions 里的四个：

| Secret | 值 |
|---|---|
| `ANDROID_KEYSTORE_BASE64` | `keystore/keystore.b64` 的内容 |
| `ANDROID_KEYSTORE_PASSWORD` | `keystore/password.txt` 的内容 |
| `ANDROID_KEY_ALIAS` | `elitemon` |
| `ANDROID_KEY_PASSWORD` | 同上（PKCS12 只有一个密码位） |

一次性设置：

```sh
gh secret set ANDROID_KEYSTORE_BASE64   < keystore/keystore.b64
gh secret set ANDROID_KEYSTORE_PASSWORD < keystore/password.txt
gh secret set ANDROID_KEY_PASSWORD      < keystore/password.txt
gh secret set ANDROID_KEY_ALIAS --body elitemon
```

要重建密钥库（本机没 JDK 也能做）：

```sh
pip install cryptography
python mobile/make_keystore.py
```

⚠️ **keystore 丢了就再也无法覆盖安装**——Android 只认同一把钥匙，换钥匙必须让用户先卸载。
把它连同密码备份到**仓库之外**（密码管理器 / 离线存储）。
⚠️ `keystore/` 已 gitignore；别把它移出这个目录，否则会连同密码一起被提交。

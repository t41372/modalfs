<p align="center">
  <img src="packaging/icon.png" width="112" alt="modalfs 图标">
</p>

<h1 align="center">modalfs</h1>

<p align="center">
  <b>将 Modal Volume 挂载为 Mac 上的本地文件夹。</b><br>
  文件按需流式加载，TB 级数据集也能瞬间打开，不占满本地硬盘。
</p>

<p align="center">
  <a href="https://github.com/t41372/modalfs/actions/workflows/ci.yml"><img src="https://github.com/t41372/modalfs/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="https://github.com/t41372/modalfs/actions/workflows/modal-live.yml"><img src="https://github.com/t41372/modalfs/actions/workflows/modal-live.yml/badge.svg" alt="对真实 Modal 的测试"></a>
  <a href="https://github.com/t41372/modalfs/actions/workflows/zizmor.yml"><img src="https://github.com/t41372/modalfs/actions/workflows/zizmor.yml/badge.svg?branch=main" alt="zizmor"></a>
  <a href="https://github.com/t41372/modalfs/releases"><img src="https://img.shields.io/github/v/release/t41372/modalfs?sort=semver" alt="最新版本"></a>
  <a href="#安装"><img src="https://img.shields.io/badge/platform-macOS%2013%2B%20%7C%20Linux-informational" alt="平台：macOS 13+ 和 Linux"></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/t41372/modalfs" alt="Go 版本"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/t41372/modalfs" alt="许可证"></a>
</p>

<p align="center"><a href="README.md">English</a> · 简体中文</p>

<p align="center">
  <img src="docs/screenshots/zh-CN/hero.jpg" alt="名为 datasets 的 Modal Volume 在访达里是一个普通文件夹，里面是 parquet 文件；后面是 modalfs 应用，列出它管理的 Volume：三个已挂载，一个正在上传 1.2 GB">
</p>

---

**modalfs** 能将 [Modal Volume](https://modal.com/docs/guide/volumes) 挂载为 Mac（或 Linux）上的本地文件夹。无论是访达（Finder）、代码编辑器、`ffmpeg` 还是本地 Agent，都能像读写普通目录一样直接操作它。数据始终保存在 Modal 云端，因此你在 Modal 上运行的计算任务访问的也是同一份文件。

> [!IMPORTANT]
> modalfs 是独立开源项目，**与 Modal Labs 没有任何关联，也未获得其官方背书**。本项目使用与 Modal 官方 SDK 相同的 API，其中部分接口未公开文档，未来可能发生变动。在用它处理重要数据前，请务必阅读[限制](#限制)章节。

> [!WARNING]
> modalfs 使用你自己的 Modal 账号运行，因此你的一切操作均受 [Modal 服务条款](https://modal.com/legal/terms)约束，违规可能导致账号被封禁。服务条款（第 1.4 与 1.5 条）明确禁止的内容包括：侵犯版权的内容、点对点（P2P）文件共享，以及为他人托管或提供媒体内容。请注意，BitTorrent（种子）下载在获取数据的同时会向其他节点上传，属于点对点文件共享。

## 目录

- [特点](#特点)
- [安装](#安装)
- [开始使用](#开始使用)
- [常见问题](#常见问题)
- [应用](#应用)
- [远程下载](#远程下载)
- [加密 volume](#加密-volume)
- [工作原理](#工作原理)
- [性能](#性能)
- [限制](#限制)
- [安全](#安全)
- [文件与设置](#文件与设置)
- [命令行](#命令行)
- [开发](#开发)
- [许可证](#许可证)

## 特点

- **多端共享同一工作区。** 你的笔记本、台式机、Modal Sandbox 和 Modal Function 均可同时挂载同一个 Volume。
- **大文件秒开。** modalfs 仅从 Modal 按需拉取应用请求的数据片段。从 100 GiB 文件的中间读取 4 KiB，只需下载一个 8 MiB 的数据块；顺序读取时则会自动并发预读后续块。
- **保存时仅上传增量块。** 修改 100 GiB 文件中的 4 KiB，仅需上传一个 8 MiB 数据块。重命名完全在云端完成（只复制文件的块列表），不传输任何数据；大文件写入在后台异步上传，期间文件夹依然流畅可用。
- **现有工具即插即用。** 挂载后就是标准的本地目录，访达、VS Code、Jupyter、`ffmpeg` 和 Blender 无需任何额外配置即可直接读写。macOS 端采用系统原生 NFS 客户端挂载，无需安装内核扩展（kext）或 macFUSE；Linux 端则基于 FUSE 挂载。
- **与云端任务实时同步。** modalfs 提交你保存的修改后，新启动的 Modal Function 就能读到（正在运行的容器需调用 `volume.reload()`）；容器提交（commit）的新文件也会在大约十秒内出现在你的本地文件夹中。
- **云端直接下载入盘。** 粘贴下载链接，即可由 Modal Function 直接将文件拉取到 Volume 中，并提供实时速率图表——期间你的 Mac 可以随时休眠或关机。由于这是唯一会消耗 Modal 算力的功能，默认处于关闭状态，需手动开启。
- **断电与崩溃保护。** 所有本地写入都会先落盘记录到日志（journal）中，直到 Modal 确认接收；即便遇到程序崩溃或意外断电也不会丢失修改。若同一文件在本地和云端同时被修改，系统会同时保留两个版本。
- **挂载点严格绑定。** 每个挂载点都与具体的账号及 Volume ID 绑定，在命令行切换 Modal CLI Profile 或重命名 Volume，都不会让挂载点静默地指向别的 Volume。
- **自定义显示标签。** 支持用空格、Emoji 或任意语言为 Volume 设置别名。标签存储在 Volume 内部，团队成员用 modalfs 挂载时都会看到相同的本地文件夹名称，而代码脚本则继续使用原本的 Modal Volume 名称。
- **可选的端到端加密。** 开启后，文件内容、文件名和标签在离开本机前均已加密。凭密钥即可在任意设备上解锁挂载。
- **轻量级原生应用与全功能 CLI。** 提供带 macOS 菜单栏图标的原生桌面端，以及覆盖全部图形界面功能的[命令行工具](#命令行)。

## 安装

### macOS

支持 macOS 13 及以上版本。前往 [Releases](https://github.com/t41372/modalfs/releases) 下载 `modalfs-<版本>-macos.zip`，解压后将 **modalfs.app** 拖入“应用程序”（Applications）文件夹即可。安装包为通用二进制（Universal Binary），原生支持 Apple Silicon 与 Intel 芯片。

由于应用目前仅做了本地（ad-hoc）签名而未经过 Apple 公证（Notarization），首次启动时可能会被 macOS 拦截。在 macOS 15 及以上版本，请先尝试打开一次，然后在**“系统设置 → 隐私与安全性”**中点击**“仍要打开”**；在 macOS 13 和 14 上，在访达中右键点击 **modalfs.app**，选择**“打开”**即可。

### Linux

前往 [Releases](https://github.com/t41372/modalfs/releases) 下载 `modalfs-gui-<版本>-linux-amd64.tar.gz` 并解压。桌面端在 Ubuntu 24.04 上构建，需要至少同样新的发行版，并依赖 WebKitGTK 4.1（`libwebkit2gtk-4.1-0`）和 FUSE 3（`fuse3` 软件包及 `/dev/fuse`）。如果是较旧的发行版、ARM64 Linux、无桌面服务器，或未安装 WebKitGTK 的环境，请直接安装[命令行版本](#命令行)：`modalfs ui` 会在本机的 `127.0.0.1` 上提供完全相同的管理界面（无桌面服务器上请通过 SSH 隧道转发该端口访问；`modalfs ui --print` 会显示链接）。

## 开始使用

1. 启动 **modalfs**。
2. 连接你的 Modal 账号：可以在 [modal.com → Settings → API Tokens](https://modal.com/settings/tokens) 生成一个 API Token 填入，也可以直接一键导入本机 Modal CLI 已有的 Profile。
3. 点击**“添加 Volume”**从账号中选择一个 Volume（添加后会立即挂载），或新建一个 Volume。挂载成功后，它会出现在 `~/ModalVolumes` 目录下，可直接在访达中打开使用。

## 常见问题

**挂载 Volume 会产生 Modal 算力费用吗？**
不会。挂载和日常文件读写仅调用常规 API，Modal 仅按标准规则收取存储费和网络流量费。只有[远程下载](#远程下载)功能会启动 Modal Function 消耗算力，且该功能默认关闭。

**云端的 Modal Function 可以和本地同时读写同一个 Volume 吗？**
可以。在云端容器内调用 `volume.reload()` 即可读取通过 modalfs 提交的最新文件；但请避免本地和云端在同一时刻写入同一个文件。

**为什么 macOS 默认用 NFS 而不是 FUSE？**
在 macOS 上使用 macFUSE，需要额外安装并在系统安全设置中允许它；若使用其内核扩展后端，Apple Silicon 机型还必须重启进入恢复模式降低系统安全策略。而 macOS 原生自带 NFS 客户端，真正做到开箱即用、零系统侵入。如果你本机已经安装了 macFUSE，也可以在对应 Volume 设置的“高级”选项中将**连接方式**切换为 FUSE。

**应用窗口必须一直开着吗？**
不需要。只要后台进程在运行即可。开启**“登录时启动”**和 Volume 的**“登录时挂载”**后，每次登录都会在后台自动完成挂载。

**电脑死机或崩溃后，我刚保存的修改去哪了？**
modalfs 会在该 Volume 下次挂载时自动重放本地日志并完成云端提交。你可以在“动态”页面看到一条“找回上次没来得及上传的……”记录；如果崩溃期间云端文件恰好被其他人改过，你的修改会被保存为冲突副本。

## 应用

| 页面         | 功能说明                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| ------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Volume**   | 集中展示所有受管 Volume 的状态（_全部都在 Modal 上_、_上传中_、_无法上传_、_有冲突待处理_、_未挂载_）、本地路径及占用空间，支持搜索并添加账号下的其他 Volume。点击进入详情页可查看实时速率与历史吞吐曲线（近 1 分钟、10 分钟、1 小时）、尚未传到 Modal 的内容、冲突列表、各子目录的容量分布，以及该 Volume 的全部设置：**标签**、**Volume 名称**、**登录时挂载**、**文件夹**、**只读**、**两边都改了同一个文件时**、密钥（仅加密 Volume），以及“高级”中的 **macOS 杂项文件只留在本地** 和 **连接方式**。 |
| **远程下载** | 添加下载链接（每个链接都会立即在自己的容器中开始），随时暂停、继续或删除。直观展示进行中任务的实时速率与每小时费用，每个下载的详情页都有速度曲线；已完成的任务会折叠进“已完成”列表，显示各自运行了多久、大约花了多少钱。                                                                                                                                                                                                                                                                                 |
| **动态**     | 按时间倒序、按日期分组展示挂载、上传提交、文件冲突、异常报错与崩溃恢复记录，并可筛选**“冲突和错误”**。窗口打开时，冲突和错误还会弹出提示。                                                                                                                                                                                                                                                                                                                                                               |
| **设置**     | 管理 Modal 账号、**登录时启动**、**挂载位置**、外观（跟随系统、浅色、深色）、界面语言（跟随系统、English、繁體中文、简体中文）与远程下载（**允许远程下载**、**容器规格**、**多久没有数据就暂停**）；在“高级”选项中可进一步调整本地缓存大小、预读和与 Modal 通信的协议。                                                                                                                                                                                                                                  |

应用窗口只是一个控制面板，实际的文件系统挂载服务运行在独立的后台守护进程（daemon）中。**关闭应用窗口绝不会卸载任何 Volume。** 在 macOS 上，modalfs 会常驻系统菜单栏，通过图标实时反映云端同步状态；点击菜单即可在访达中快速打开已挂载的 Volume，或随时呼出主窗口。Linux 上没有菜单栏图标，所以第一次在有 Volume 挂载时关闭窗口，modalfs 会说明它们仍保持挂载，并提供直接退出的选项。

**退出应用**（按 `⌘Q`，或在菜单栏及窗口内点击“退出”）则会执行完整清理：先将待上传的数据全部同步至云端，逐一安全卸载所有 Volume，最后关闭后台进程。如果某个 Volume 正被其他软件占用，你可以选择**“重试”**或**“强制卸载”**，也可以随时点击**“立即退出”**停止等待——立即退出不会丢失任何数据，未上传完的改动会妥善保存在本地日志中，并在下次挂载时自动续传。远程下载在 modalfs 退出后仍会在 Modal 上继续运行（并继续计费）。

<table>
  <tr>
    <td width="50%" valign="top">
      <a href="docs/screenshots/zh-CN/volume.jpg"><img src="docs/screenshots/zh-CN/volume.jpg" alt="深色主题的 Volume 页面：挂载位置、大小、当前和最近一分钟的上传和读取速度"></a>
    </td>
    <td width="50%" valign="top">
      <a href="docs/screenshots/zh-CN/activity.jpg"><img src="docs/screenshots/zh-CN/activity.jpg" alt="动态页：上传、挂载和一次恢复，按天分组"></a>
    </td>
  </tr>
  <tr>
    <td width="50%" valign="top">
      <a href="docs/screenshots/zh-CN/settings.jpg"><img src="docs/screenshots/zh-CN/settings.jpg" alt="深色主题的设置页：Modal 账号、登录时启动、挂载位置、外观、语言和远程下载"></a>
    </td>
    <td width="50%" valign="top">
      <a href="docs/screenshots/zh-CN/download.jpg"><img src="docs/screenshots/zh-CN/download.jpg" alt="一个远程下载：进度、速度和目前的花费，全程速度图（标出了 Modal 把它换到新容器的那段时间）以及经过"></a>
    </td>
  </tr>
</table>

## 远程下载

只需向 modalfs 提供一个下载链接，你工作区中的 Modal Function 就会在云端直接将文件下载到目标 Volume 内。下载期间，你的电脑可以随时合盖休眠、断网甚至关机。

- **默认关闭，按需启用。** 由于这是 modalfs 唯一会消耗 Modal 算力的功能，需在“远程下载”页面或“设置”中手动开启。Function 仅在你添加或恢复下载任务时启动，并在任务完成、暂停或超时停滞后立即自动销毁。
- **费用透明低廉。** 默认的「小」规格（`small`，0.125 CPU、512 MiB 内存）在下载期间每小时算力成本约 $0.01 起（实际 CPU 用量高于申请值时，Modal 按实际用量计费，跑得快的下载会更贵），外加 Modal 标准网络流量费；下载本身还会产生约为下载量 2% 的 Modal 出站流量。首次使用（或每次升级 modalfs 后首次下载）时，系统会启动一个临时 Sandbox 将下载 Function 部署到你的工作区——第一次需要构建镜像，约一分钟，之后约 10 秒、花费约 $0.0001，本机无需安装 Python 环境。
- **内置三大下载引擎。** 集成 `gopeed`、`aria2` 与 `yt-dlp`，其中 `yt-dlp` 每次运行都会更新到最新版（网站变化得比 modalfs 更新得快）。modalfs 会根据链接类型自动匹配最佳工具（也可手动指定）；当 `gopeed` 在某个源站上连续失败时，会自动切换由 `aria2` 接管，从头重新下载该文件。
- **遵守 Modal 服务条款。** 请仅下载你拥有合法授权的内容。由于 BitTorrent 协议在下载同时会向其他节点上传数据，属于 [Modal 服务条款](https://modal.com/legal/terms)（第 1.5 条）明令禁止的点对点文件共享行为，下载种子可能导致你的 Modal 账号被封禁。详见[已知问题](docs/known-issues.md#bittorrent-uploads-while-it-downloads)。
- **容器中断自动续传。** 当 Modal 因资源调度回收容器时，由于下载进度保存在 Volume 中，新容器通常会在一分钟内接管并从断点处继续下载。单个 Function 最长运行 24 小时，超长任务会自动换到新容器继续。所有容器切换记录都会清晰展示在任务详情页中。
- **高频实时监控。** 打开任务详情页时，速率图表每秒刷新 4 次。任务状态、事件日志和历史速度曲线均持久化保存在 Volume 的 `.modalfs/downloads/` 目录下。
- **支持 Cloudflare WARP 代理。** 针对屏蔽云服务商机房 IP 的网站，可在“网络”中选择通过 Cloudflare WARP 隧道下载。速度会有所下降，且下载器将强制仅通过 WARP 通信——如果连不上 WARP，下载会停止而不会回退为直连，你可以随后不经 WARP 重试；下载途中隧道断开时，下载会停滞并暂停，同样不会改为直连。
- **防覆盖保护。** 下载完成的文件若与目录中已有文件重名，会自动追加 ` (1)` 后缀，绝不覆盖旧文件。
- **月度开销统计。** “远程下载”页面会展示整个工作区（存储、流量与算力）和远程下载各自的本月花费。由于 Modal API 未开放剩余赠金（Credit）和消费限额查询，界面提供了直达 Modal Usage & Billing 页面的快捷链接。若下载被 Modal 拒绝或强行终止，详情页会明确展示报错原因（或注明 Modal 未返回原因）。

关于远程下载目前尚存的边界情况，请参阅[已知问题](docs/known-issues.md)。

## 加密 volume

新建 Volume 时可开启端到端加密（E2EE）。文件内容、文件名和 Volume 标签在离开你的电脑前就已完成本地加密，因此 Modal 官方或任何拥有该工作区访问权限的人员看到的都只是密文；而在你的电脑上，挂载后的文件夹依然像普通目录一样透明可用。

创建 Volume 时开启**“端到端加密”**，modalfs 会生成并展示一串以 `mfs1-` 开头的专属密钥。**请务必立即妥善备份该密钥（例如存入密码管理器）。** 只要拥有这串密钥和 Modal 上的密文数据，你就可以在任何电脑上完整还原所有文件，即使本机上 modalfs 自己的文件都已丢失。一旦丢失密钥，没有任何人（包括 modalfs）能恢复你的数据——不能用口令代替密钥，也没有任何密钥找回机制。

- **本机自动解锁。** 当前电脑会将密钥安全保存在本机（macOS 钥匙串、Linux Secret Service 或权限为 `0600` 的私有文件），实现自动挂载。在其他设备上，该 Volume 会显示为“已锁定”，点击**“解锁”**并粘贴密钥即可挂载。
- **防护边界说明。** 端到端加密保护的是存储在 Modal 云端和传输途中的数据，而非已解锁电脑上的本地数据。本地块缓存、尚未上传的修改日志以及冲突副本在本机磁盘上仍为明文，即便点击“忘记密钥”也不会自动销毁。将电脑转交他人前，请先等待所有修改上传完毕，对每个加密 Volume 选择**“忘记这台电脑上的密钥”**，再在“设置”中执行**“清除缓存”**（清除缓存不会删除尚未上传的修改）。
- **云端计算限制。** Modal Function 无法直接读取加密 Volume 中的内容，远程下载功能也无法将文件存入加密 Volume。
- **元数据可见性。** Modal 仍可获知该 Volume 在 Modal 上的名称、目录层级结构、文件与文件夹数量、文件大小（并可由此精确推算文件内容和文件名的长度）、时间戳、文件何时被修改，以及哪些 8 MiB 密文块完全相同。
- **使用限制。** 加密后的单个文件名最长支持 147 字节（约 49 个汉字）；重命名文件夹时，需要为其中每个文件发送一次请求；仅支持在新建 Volume 时开启加密，且创建后无法更换密钥。

加密格式的设计细节与安全取舍已完整公开在 [docs/design/encryption.md](docs/design/encryption.md) 中，包含脱离 modalfs 独立解密 Volume 所需的全部规范。

## 工作原理

<p align="center">
  <img src="docs/diagrams/how-it-works.zh-CN.svg" alt="你电脑上的应用使用一个普通文件夹。modalfs 通过 NFS 或 FUSE 提供这个文件夹，并在本机磁盘上保存缓存和日志。它通过 Modal 的 Volume API 列目录和提交文件，与 Modal 的块存储之间传送 8 MiB 的块，你的 Modal Function 读的也是这些块。">
</p>

在 macOS 上，modalfs 在本地 `127.0.0.1` 启动一个回环 NFS 服务端，由 macOS 原生 NFS 客户端挂载；在 Linux 上则直接通过 FUSE 提供挂载。两种挂载方式背后共用同一个后台守护进程，统一管理本地读取缓存、未上传修改的持久化日志（journal），以及已知数据块的哈希索引。

**Modal Volume v2 中的每个文件本质上是一串按序排列的 8 MiB 数据块（Block）**，每个数据块由其内容的 SHA-256 哈希值唯一标识。提交文件时，modalfs 会将该文件的块哈希列表发送给 Modal；Modal 返回其中云端尚未存储的缺失块列表，modalfs 随后仅上传这些缺失块。相同内容的数据块会跨文件、跨 Volume 自动去重。读取文件时，Modal 会为指定区间内的每个数据块返回预签名下载 URL。服务端复制只需复制块哈希列表，以这种方式复制一个 100 GiB 的大文件仅需 2.9 秒；modalfs 的重命名用的就是它。

因此，局部修改一个大文件，只需重新计算被改动块的 SHA-256 哈希，保留其余未改动块的哈希，并上传新增的数据块即可。但由于 Modal API 从不返回云端已有文件的块哈希列表，modalfs 通过以下三种机制来维护块哈希：

1. **本机哈希记录。** modalfs 从云端读取的每一个完整 8 MiB 数据块，都会在本地计算哈希值，并与文件路径、大小及修改时间一并记录在本地索引中。
2. **Volume 内置清单（Manifest）。** 对于由 modalfs 写入且大于等于 64 MiB 的文件，系统会将完整的块哈希列表保存在 Volume 内部的 `/.modalfs/manifests/` 目录下。这样其他电脑（或清空本地缓存后）只要核对文件大小与修改时间一致，即可直接复用清单进行快速局部修改。
3. **按需读取算哈希。** 对于尚未建立哈希清单的大文件（例如刚刚由云端 Modal 容器生成的文件），提交时仍需要每个未改动块的哈希。你一开始编辑这类文件，modalfs 就会在后台把这些块拉取一遍并计算哈希，因此首次局部修改时，文件的其余部分都会被完整读取一次。

**大文件边写边传与分段提交。** 在复制大文件时，任何一个 8 MiB 数据块只要停止变动满 0.5 秒，就会立即在后台开始上传，并采用“前缀分段提交”策略：每次将从文件开头到第一个尚未传到 Modal 的已改动块之间的连续前缀先行提交给 Modal，因此提交每一段时都无需再上传数据。每次分段提交都是文件在 8 MiB 块边界处截断的开头部分，绝不会出现新旧数据混杂。由于每个块的上传在 Modal 端都有约 1.5 秒的固定开销，单条连接通常只有几 MiB/s 的速率，系统会根据你的网络状况动态维持 4 到 32 个并发上传流（目标是让每个块在 5 秒左右传完），从而充分跑满带宽。

**对未公开 API 行为的依赖与兜底机制。** 上述“未提交先上传（预上传）”机制依赖于实测验证的三项 Modal API 特性（官方未做文档承诺）：

1. 提交文件时若将第一个块的哈希设为虚构值，Modal 会正常下发其余块的上传 URL，但不会真正完成文件提交。
2. 通过上述方式预先上传的数据块，可以在后续提交该 Volume 内任意文件时复用，但必须携带上传成功时返回的回执（Receipt）（modalfs 仅使用 10 分钟内的回执，曾观察到 15 分钟前的回执依然有效）。
3. 若上传的数据内容与声明的 SHA-256 哈希不匹配，Modal 会直接拒收。

自动化测试每天都会在真实的 Modal 环境中验证上述三项行为。如果 Modal 不再“只下发上传 URL 而不提交”，modalfs 会关闭预上传、在“动态”页面说明，并改为在提交时上传每个文件；如果某个回执被拒，该次提交会直接重新上传这些块。两种情况都不会丢失数据，其余变化由每日测试负责发现。需要注意的是，如果某些块已预先上传，但文件在正式提交前就被删除或中止，这些未引用的块会留在 Modal 的块存储中；Modal 官方目前未说明是否会定期清理这类游离块或对其计费。

其他实现细节：

- **空文件夹支持。** Modal 原生不支持空目录，因此新建空文件夹时，modalfs 会自动在其中写入一个隐藏的 `.modalfs_keep` 占位文件。
- **重命名。** 重命名文件实际上是复制一份块哈希列表并删除旧路径，因此通过“写入临时文件再重命名”来保存文件的编辑器都能正常工作。
- **冲突检测与保护。** 每次向云端提交前，modalfs 都会再次核对 Modal 上的文件状态。如果在你编辑期间该文件已被其他端修改，你的本地版本会另存为同目录下的 `文件名.conflict-YYYYMMDD-HHMMSS.扩展名`（也可在设置中改为直接覆盖）。
- **屏蔽 macOS 临时文件。** `.DS_Store` 和 `._*` 等 macOS 系统生成的元数据文件仅保存在本地内存中，永远不会上传污染云端 Volume（可按 Volume 单独关闭此过滤）。
- **独立账号管理与防错挂。** modalfs 维护独立的账号列表，并将 Token Secret 安全存储在系统钥匙串中。后台进程不会跟随 Modal CLI 当前激活的 Profile，仅在你主动执行导入时读取一次 `~/.modal.toml`（不经过后台进程的调试命令 `modalfs run` 和 `modalfs serve` 例外）。每个挂载点都会持久化记录所属账号、环境、Volume 名称及唯一的 Volume ID：即使 Volume 在云端被改名，modalfs 也能通过 ID 精准识别；反之，如果原 Volume 被删除后又新建了一个同名 Volume，modalfs 会拒绝挂载并报错，防止将不同的数据集静默挂载到同一本地目录下。

## 性能

以下数据在 Linux arm64 沙箱环境中测得，测试网络经过带宽受限的 HTTP/1.1 代理（单连接限速约 7 MB/s）。在普通家庭或办公宽带下表现会更好。

| 测试操作                                        | 实测结果                                              |
| ----------------------------------------------- | ----------------------------------------------------- |
| 在 100 GiB 文件的 77 GiB 偏移处冷启动读取 4 KiB | **1.45 秒**（随后再次读取：**0 毫秒**，直接命中缓存） |
| 顺序读取 512 MiB 文件                           | **40.9 MiB/s**（受沙箱单连接 ~7 MB/s 下载限速影响）   |
| 修改 100 GiB 文件中的 4 KiB 内容                | 上传 8 MiB，约 **3.5 秒**完成提交                     |
| 修改 50 MB 文件中的 4 KiB 内容                  | 上传 1 个块，**2.0 秒**完成                           |
| 修改一个从未读取过的 20 MB 文件中的 5 字节      | 拉取 2 个块计算哈希，上传 1 个块，耗时 **3.7 秒**     |
| 服务端复制 100 GiB 文件（重命名用的就是它）     | **2.9 秒**，零数据传输                                |
| 写入途中执行 `kill -9` 强杀进程并重启           | 自动从本地日志恢复修改并完成提交，文件校验和完全一致  |

## 限制

### Modal API 本身的限制

modalfs 基于 Modal SDK 所用的 Volume API 构建，其底层能力少于在 Modal 容器内部直接挂载 Volume。每日自动化测试会持续监控这些接口行为——如果未来 Modal 支持了以下任何特性，欢迎[提交 Issue](https://github.com/t41372/modalfs/issues)。

- **不支持分布式文件锁。** Modal 没有提供比较并交换（CAS）原语，因此当多端同时写入同一个文件时，最后一次提交会覆盖先前的提交。modalfs 会在提交前尽可能检测远端变动，并将冲突修改另存为副本。
- **不保留文件权限（Permissions）。** Modal 会忽略上传请求中的权限位，因此所有通过 modalfs（或 `modal volume put`）保存的文件，在云端容器内权限统一为 `0666`。这意味着你在本机保存的可执行脚本，在 Sandbox 中直接运行会报 `permission denied`，且容器内的 `git` 会检测到文件权限变更。解决方法：在 Sandbox 中重新执行 `chmod +x`，并在 Volume 内的 Git 仓库中设置 `git config core.fileMode false`。在本机端，modalfs 会静默放行 `chmod`、`chown` 和 `utimes` 系统调用（不报错但也不生效），以确保 `cp -p`、`rsync` 等工具能顺利运行。
- **不支持读写符号链接（Symlinks）。** 云端容器内创建的符号链接在本地也会显示为符号链接，但 Modal API 不支持读取其指向的目标路径，也无法创建、修改、移动或删除符号链接。在本地打开符号链接会报 `Operation not supported`（macOS 上显示为 `Input/output error`），每次挂载后第一次遇到时会在“动态”页说明原因。请直接在 Modal 容器内管理符号链接。此外，通过 modalfs 执行 `git clone` 时，Git 会将符号链接回退写成普通小文本文件，并自动在仓库配置中写入 `core.symlinks=false`——因此大量使用符号链接的代码仓库请直接在 Sandbox 容器内部执行 `git clone`。
- **不支持硬链接（Hard links）。** 本地无法创建硬链接；对于云端容器内创建的硬链接，在本地修改并保存其中任意一个路径后，该文件就会与原硬链接分离成独立文件。
- **修改时间由云端决定。** 文件的修改时间（`mtime`）由 Modal 在提交完成时统一设置。
- **最小写入粒度为 8 MiB。** 哪怕只随机修改几个字节，也需要上传完整的 8 MiB 数据块。
- **云端容器需手动刷新。** 正在运行的 Modal 容器需要调用 `volume.reload()` 才能看到通过 modalfs 提交的新改动。
- **依赖未公开文档的 API。** modalfs 依赖这套 API 中 Modal 没有写进文档的部分；若 Modal 官方做不兼容变更，在 modalfs 发布更新前可能暂时无法使用。

### modalfs 目前的限制

- **仅支持 Volume v2。** 旧版 v1 Volume 会在列表中显示，但无法挂载。
- **读取期间可能发生版本切换。** 目录列表每 10 秒刷新一次；如果你正在读取某个大文件，而该文件恰好在别处被修改提交，后续读取的数据块可能会中途切换到新版本。
- **编辑期间文件在 Modal 上被替换。** 如果你正在编辑的文件，在 modalfs 读取你未改动的部分之前就在 Modal 上被替换或删除，或者在 modalfs 读过其中一部分之后被替换成**字节大小相同**的新文件，系统将无法判断那些部分属于哪个版本。为了避免拼凑出一个新旧混杂的损坏文件，modalfs 不会生成普通冲突副本，而是将你修改过的 8 MiB 数据块单独保存在本机，附带一份拼接说明文档，并在“动态”页面告知保存位置。
- **不支持扩展属性（xattr）。** 系统不会保存文件的 Extended Attributes。
- **菜单栏图标仅限 macOS。** 在 Linux 上关闭窗口后，屏幕上不会再显示 modalfs 仍在运行。再次打开应用即可回到界面（或运行 `modalfs ui` 在浏览器标签页中打开同样的界面），要停止它，请在界面中选择**“退出”**或运行 `modalfs quit`。
- **项目仍处于早期阶段。** 遇到任何问题欢迎随时提交 Issue 反馈。

### 使用须知

- **回写延迟（Write-back delay）。** 在 NFS 挂载（macOS 默认）上，modalfs 看不到文件何时被关闭，因此会在文件停止写入约 3 秒后将其提交至 Modal；若文件持续写入，则至少每 30 秒提交一次（大文件复制约每 10 秒提交一次已完成的开头部分）。在 FUSE 挂载上，关闭文件后几乎立即提交。遇到崩溃时未提交的数据会从本地日志自动恢复。唯一例外：应用在最后一秒追加写入（append）且未调用 `fsync` 同步的内容，其末尾的零字节（`0x00`）在恢复时可能会丢失。
- **安全卸载等待。** 卸载 Volume 时会等待未上传完的数据传完，并显示实时进度。点击**“立即卸载”**则会立刻断开挂载，剩余数据继续在后台上传（或留到下次挂载时上传）。如果有应用正打开该文件夹中的文件，modalfs 会明确提示是哪个应用，**“强制卸载”**则会照样卸载。
- **小内存 Mac 复制大文件时的周期性停顿。** 在内存较小（如 8 GB）的 Mac 上，macOS 内核的 NFS 客户端在复制大文件时会自行每隔 30 秒才集中刷出一批写入（在 7.5 GB 内存的机器上每批约 100 MB），期间复制进度条与访达会出现短暂等待。详见[已知问题](docs/known-issues.md#large-copies-pause-on-a-mac-with-little-memory)。
- **后台进程需保持运行。** 只要有 Volume 处于挂载状态，后台守护进程就必须保持运行以提供文件系统服务。若后台进程被意外强杀，下次启动时会自动清理残留的失效挂载点。

## 安全

- **凭据安全存储。** API Token Secret 保存在 macOS 钥匙串（Keychain）或 Linux Secret Service 中；在无桌面环境的 Linux 上则存入权限为 `0600` 的 `secrets.json`。密钥绝不会写入任何日志文件。
- **本地通信与 Web 界面防护。** 后台进程通过仅当前用户可读写的 Unix 套接字（Socket）与桌面端及命令行通信，并在 `127.0.0.1` 的随机端口上为浏览器提供本地 Web 服务。运行 `modalfs ui` 打开的链接包含一次性 Token，验证后即换取会话 Cookie；此外每个请求都必须带有回环 `Host` 头，每个 API 请求还必须带有自定义请求头，有效抵御跨站请求伪造（CSRF）和 DNS 重绑定攻击。
- **NFS 回环隔离。** 内置 NFS 服务端仅监听 `127.0.0.1`，且仅响应携带当前用户 UID（或 root）的请求，防止同一台 Mac 上的其他系统账户挂载你的 Volume。关于本地安全边界的更多说明，请参阅[已知问题](docs/known-issues.md)。
- **危险操作二次确认。** 删除 Volume 需要输入其名称确认，处于已挂载状态的 Volume 禁止删除。移除账号前也会先确认（`modalfs account rm` 同样会询问，除非加上 `--yes`）；账号下仍有已挂载的 Volume 或正在运行的远程下载时，无法移除。
- **安全漏洞上报。** 如发现安全漏洞，请按照 [SECURITY.md](SECURITY.md) 中的指引私下联系我们，请勿在公开 Issue 中披露。

## 文件与设置

| 用途                         | macOS 路径                                        | Linux 路径                               |
| ---------------------------- | ------------------------------------------------- | ---------------------------------------- |
| 配置文件、账号、套接字与日志 | `~/Library/Application Support/modalfs/`          | `~/.config/modalfs/`                     |
| 数据块缓存与写入日志         | `~/Library/Caches/modalfs/`                       | `~/.cache/modalfs/`                      |
| 默认挂载根目录               | `~/ModalVolumes/`                                 | `~/ModalVolumes/`                        |
| 登录时启动服务配置           | `~/Library/LaunchAgents/dev.modalfs.daemon.plist` | `~/.config/systemd/user/modalfs.service` |

在 Linux 上，由 systemd 在登录时启动的后台进程会把日志写入用户 journal（`journalctl --user -u modalfs`），而不是日志文件。

**缓存大小与流控机制。** “缓存大小”限制了 modalfs 在本机占用的最大磁盘空间总和（包含所有 Volume 共用的读取缓存块，以及尚未传到 Modal 的本地修改块）。系统优先保障本地写入，必要时会自动淘汰已缓存的读取块来腾出空间。当未上传的修改占满缓存上限时（系统会固定保留最多 1/4 且不超过 1 GiB 的空间供读取使用），新的写入请求会暂停等待，直到后台上传完成并释放空间。

因此，即便复制一个远大于缓存上限的超大文件，也绝不会撑爆你的本地硬盘——写入速度会自动匹配你的网络上传带宽。由于大文件采用边写边传、分段提交的设计，每个分段提交后就会立即从本地磁盘释放，因此写入阻塞时通常只需等待一两个 8 MiB 块传完，而不必等整个文件传完。不过在 macOS 上，一旦触发写入等待，系统自带的 NFS 客户端可能会将写入请求压住 20–30 秒才重试；为了获得最流畅的大文件复制体验，建议将缓存上限设置得比你一次复制的总量更大。此外，当本机磁盘剩余可用空间不足 1 GiB 时，写入同样会暂停等待；若没有任何待上传数据能腾出空间（例如磁盘已满，或缓存被未挂载 Volume 的修改占满），写入会立即报错返回，并在“动态”页面记录错误。

当网络断开无法连接 Modal 时，正在向 Volume 写入数据的应用会进入等待状态（在 macOS 上持续数分钟后会超时放弃，行为与普通网络硬盘断线一致）。此前已写入本地的数据会完好保存在本机日志中，待网络恢复后自动继续上传，应用界面也会明确提示当前无法上传及具体原因。

缓存大小可设置为 `1` GB 以上的任意整数。默认值为 **20 GB**，建议至少设置为 **2 GB**（计算公式约为：`2 × 并发读取文件数 × (预读块数 + 1) × 8 MiB + 256 MiB`）。

## 命令行

虽然日常操作通过图形界面即可完成，但 `modalfs` 命令行工具（CLI）提供了与桌面端完全对等的全部功能（包括“动态”记录：`modalfs activity`），两者底层连接同一个后台进程，非常适合终端重度用户、自动化脚本与无桌面 Linux 服务器。

### 安装

在 macOS 上，桌面应用包内已内置命令行程序。执行以下命令将其软链接到系统 `PATH`：

```sh
sudo ln -sf /Applications/modalfs.app/Contents/Resources/bin/modalfs /usr/local/bin/modalfs
```

如果只需要独立的命令行程序，可前往 [Releases](https://github.com/t41372/modalfs/releases) 下载对应平台的压缩包 `modalfs-<版本>-darwin-arm64.tar.gz`（或 `-darwin-amd64`、`-linux-amd64`、`-linux-arm64`）：

```sh
tar -xzf modalfs-*-linux-amd64.tar.gz
sudo install modalfs-*-linux-amd64/modalfs /usr/local/bin/modalfs
```

Linux 环境需确保已安装 FUSE 3（`fuse3` 软件包及 `/dev/fuse`）。如需从源码构建，请克隆仓库后运行 `just build`（见[开发](#开发)）。在仓库中直接 `go build ./cmd/modalfs` 不会包含前端界面，执行 `modalfs ui` 时会显示提示说明页面；`go install …@latest` 无法使用，因为 modalfs 依赖 `third_party/` 中打过补丁的 go-nfs。

### 快速上手

1. **添加账号。** 在 [modal.com → Settings → API Tokens](https://modal.com/settings/tokens) 创建一个 Token，或直接导入本地 Modal CLI 配置：

   ```sh
   modalfs account add                    # 交互式提示输入 Token ID 和 Secret
   modalfs account import                 # 或一键导入 ~/.modal.toml 中的 Profile
   ```

2. **创建并挂载 Volume：**

   ```sh
   modalfs volume create my-data
   modalfs mount my-data                  # 默认挂载至 ~/ModalVolumes/my-data
   ```

3. **读写文件。** 在访达、编辑器或终端中直接操作该文件夹。文件停止变动约 3 秒后，修改会自动提交至云端：

   ```console
   $ echo hi >> ~/ModalVolumes/my-data/notes.md
   $ modalfs status
   VOLUME   LABEL  ACCOUNT  STATE    MOUNTPOINT                      PENDING  DOWN/UP
   my-data  -      work     mounted  /Users/me/ModalVolumes/my-data  0        0 B / 3 B

   on this computer: 0 B cached + 0 B not uploaded yet, of 20.0 GB
   ```

4. **用完卸载。** 卸载前会自动将待上传的内容全部同步完毕：

   ```sh
   modalfs unmount my-data
   ```

modalfs 会自动记住每个挂载点的配置，下次只需运行 `modalfs mount my-data`，就会自动沿用上次的环境和挂载目录（只有一个账号时也会沿用账号；有多个账号时需再次传入 `--account`）。后续挂载时若传入新选项（如 `--ro`），则会更新已保存的配置。

### 命令参考

```
日常
  modalfs ui [--print]                        打开应用（需要时启动后台进程）；
                                              --print 改为显示浏览器地址
  modalfs status                              各个挂载及其上传情况
  modalfs mount VOLUME [MOUNTPOINT]           挂载并记住它
        [--account A] [--env E] [--ro] [--auto] [--mode auto|nfs|fuse] [--keep-junk]
        [--on-conflict keep-both|overwrite] [--write-back 3s]
  modalfs unmount VOLUME|PATH [--now]         上传待上传的内容，然后卸载；
                                              --now 立即卸载，剩下的在后台继续上传
  modalfs sync VOLUME|PATH                    立即上传待上传的内容
  modalfs forget VOLUME|PATH                  删除保存的挂载（volume 本身不受影响）
  modalfs info VOLUME|PATH [--measure]        大小、等待上传的文件、未处理的冲突；
                                              --measure 列出整个 volume 来计算大小
  modalfs conflicts VOLUME|PATH [--handled]   冲突副本；--handled 全部标为已处理
  modalfs activity [--problems] [--follow]    “动态”记录：挂载、上传、冲突、错误、找回；
                                              --problems 只显示冲突和错误

Volume
  modalfs volume ls      [--account A] [--env E]
  modalfs volume create  NAME [--label L] [--account A] [--env E] [--mount]
  modalfs volume rename  NAME NEWNAME [--account A] [--env E]
  modalfs volume label   NAME [LABEL | --clear]   代替 NAME 显示的名字，存在 volume 里
  modalfs volume rm      NAME [--account A] [--env E] [--yes]

加密 volume
  modalfs volume create  NAME --encrypt       显示新 volume 的密钥；存好之后再继续
  modalfs unlock VOLUME [--remember=false]    用密钥解锁（手动输入，或从 stdin 读取）；
                                              这台电脑会记住它，除非加 --remember=false
  modalfs lock VOLUME                         让这台电脑忘掉密钥（先卸载）
  modalfs volume key     NAME                 显示密钥（仅限记住了它的电脑）
  modalfs volume decrypt NAME --to FOLDER     只用密钥，把所有文件解密复制出来

账号
  modalfs account add [--label L] [--token-id ID] [--env E] [--secret-from-env]
  modalfs account import [PROFILE...]
  modalfs account ls
  modalfs account check ACCOUNT               问 Modal 这个 token 是否仍然有效
  modalfs account token ACCOUNT [--token-id ID]   更换过期的 token（必须是同一个工作区）
  modalfs account rename ACCOUNT NEWNAME
  modalfs account env ACCOUNT ENVIRONMENT     添加 volume 时在哪个环境里查找
  modalfs account rm ACCOUNT [--yes]          忘记 token 和它保存的挂载（会先询问）

远程下载（打开之前是关着的；每个下载都在 Modal Function 里运行，向你的账号计费）
  modalfs download enable|disable             打开或关闭远程下载
  modalfs download add VOLUME[:/FOLDER] URL...
        [--downloader gopeed|aria2|yt-dlp] [--size small|medium|large] [--cookies FILE]
        [--warp | --no-warp] [--account A] [--env E]
  modalfs download ls
  modalfs download show ID [--follow]         进度、执行次数和中断、速度
  modalfs download pause ID
  modalfs download resume ID [--warp | --no-warp]
  modalfs download cancel ID [--keep]         停止并删除下载了一半的文件（--keep 保留它们）
  modalfs download logs ID                    下载工具的日志
  modalfs download rm ID                      从列表移除（下载好的文件保留）
  modalfs download spend [--account A] [--env E]
                                              工作区和它的下载本月花了多少钱

后台进程
  modalfs daemon                              在前台运行后台进程
  modalfs quit [--now]                        上传所有内容、卸载、停止后台进程；
                                              --now 立即停止（待上传的内容在下次挂载时上传）
  modalfs service install|uninstall           登录时启动（LaunchAgent / systemd 用户单元）
  modalfs settings [NAME VALUE]               显示设置，或改其中一项：cache-size、read-ahead、
                                              mount-root、transport、download-size、download-stall
  modalfs cache clear                         删除块缓存（还没上传的修改会保留）
  modalfs version                             版本和平台

不经过后台进程（脚本、调试）
  modalfs run [flags] VOLUME MOUNTPOINT       在前台挂载，直到 Ctrl-C（modalfs run -h 列出所有选项）
  modalfs serve [flags] VOLUME                只启动 NFS 服务，并打印挂载命令
```

`run` 和 `serve` 使用的是 Modal CLI 的凭据（`~/.modal.toml` 中当前激活的 Profile、`--profile`，或 `MODAL_TOKEN_ID`/`MODAL_TOKEN_SECRET`），而不是添加到 modalfs 的账号。

`--account` 参数支持传入账号别名、账号 ID 或工作区名称；当本机只配置了一个账号时可省略。

远程下载命令示例：

```console
$ modalfs download enable
$ modalfs download add datasets:/raw https://example.org/train-2024.tar
$ modalfs download show dl-3f2a --follow
```

### 环境变量

| 变量名                    | 作用                                                                                                                      |
| ------------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| `MODALFS_HOME`            | 自定义配置文件与缓存的存放目录（便于测试隔离）                                                                            |
| `MODALFS_TRANSPORT=http1` | 强制使用 HTTP/1.1 请求 Modal API（适用于仅支持 HTTP/1.1 的代理环境）                                                      |
| `MODALFS_NO_KEYCHAIN=1`   | 将密钥明文保存在权限为 `0600` 的 `secrets.json` 中，不使用系统钥匙串                                                      |
| `MODALFS_DEBUG=1`         | 让用 `modalfs daemon` 启动的后台进程输出调试日志（需先退出正在运行的后台进程）                                            |
| `MODAL_TOKEN_SECRET`      | 在脚本中执行 `modalfs account add` 时提供 Token Secret，或供以 `--secret-from-env` 添加的账号使用（后台进程必须能读到它） |
| `MODAL_CONFIG_PATH`       | `modalfs account import` 读取 Modal CLI Profile 的位置（默认 `~/.modal.toml`）                                            |

## 开发

开发环境需安装 Go 1.26+、用于构建前端界面的 [Bun](https://bun.sh) 以及任务运行器 [just](https://github.com/casey/just)。编译 macOS 桌面应用还需安装 Xcode 命令行工具（`xcode-select --install`）；编译 Linux 桌面应用则需安装 `libgtk-3-dev` 和 `libwebkit2gtk-4.1-dev`。直接运行 `just` 可查看所有可用构建任务：

```sh
git clone https://github.com/t41372/modalfs && cd modalfs
just app             # 构建 macOS 通用应用包：dist/modalfs.app（支持 Apple Silicon 与 Intel）
just linux-gui       # 构建 Linux 桌面应用：dist/modalfs-gui
just cli             # 交叉编译 macOS 与 Linux 命令行程序：dist/modalfs-{darwin,linux}-{arm64,amd64}
just build           # 编译当前机器架构的命令行程序：dist/modalfs
just dev-mock        # 使用模拟数据启动带热重载的前端界面（无需后台进程和 Modal 账号）
just dev             # 连接本机正在运行的后台进程，启动带热重载的前端界面
just lint            # 运行 gofmt、go vet，以及前端类型检查与多语言翻译校验
just test            # 运行带竞态检测的单元测试（离线，无需 Modal 账号）
just test-e2e        # 连接模拟弱网的假 Modal 服务端，运行真实挂载测试（macOS 为 NFS，Linux 为 FUSE）
just fake -up 20M    # 启动本地的假 Modal 服务端，无需账号即可体验测试 modalfs
MODALFS_TEST_VOLUME=scratch just test-integration   # 运行真实环境集成测试（注意：会在指定 volume 内创建和删除文件）
```

不支持 macOS 12。用 Go 1.26 时，`MIN_MACOS=12.0 just app` 能做出以 macOS 12 为目标的应用，但没有测试过；Go 1.27 及更高版本完全不能在 macOS 12 上运行。

前端界面基于 React、Tailwind CSS 和构建在 Base UI 之上的 shadcn/ui 开发，视觉设计延续了 [Jezo](https://github.com/t41372/Jezo) 的风格。执行 `just ui` 会将前端资源打包至 `internal/webui/dist`，随后由后台进程和桌面应用通过 Go embed 嵌入二进制文件中。

README 中的所有配图均由脚本自动生成：执行 `cd web && bun run screenshots` 会基于 `web/src/api/mock.ts` 中的示例数据自动渲染并截取各界面截图；执行 `bun run diagrams` 则会生成“工作原理”架构图。首屏主图（Hero Image）中的真实访达窗口，是由 `web/scripts/capture-finder.sh` 挂载本地的假 Modal 服务后实景截取合成的。

| 目录                                      | 模块说明                                                       |
| ----------------------------------------- | -------------------------------------------------------------- |
| `cmd/modalfs`                             | 命令行工具与后台守护进程入口                                   |
| `cmd/modalfs-gui`                         | 桌面端原生应用（基于 Wails），支持拉起后台进程                 |
| `internal/modalapi`                       | Modal API 客户端、数据块并发上传与下载实现                     |
| `internal/downloads`                      | 远程下载：任务、Modal Function 及其工作进程（`remote/app.py`） |
| `internal/encryption`                     | 文件内容、文件名与标签的端到端加密                             |
| `internal/vfs`                            | 虚拟文件系统核心：块缓存、增量修改追踪、持久化日志与冲突处理   |
| `internal/nfsfront`、`internal/fusefront` | 本地 NFSv3 与 FUSE 文件系统服务端实现                          |
| `internal/daemon`                         | 挂载生命周期管理、本地 JSON API 服务与登录时启动配置           |
| `internal/config`                         | 账号管理、挂载点持久化配置、全局设置与系统钥匙串集成           |
| `internal/modalfake`、`cmd/modalfs-fake`  | 测试和 `just fake` 使用的假 Modal 服务端                       |
| `internal/e2e`                            | 真实挂载 modalfs 的端到端测试                                  |
| `third_party/go-nfs`                      | NFS 服务端所基于的、打过补丁的 go-nfs                          |
| `web`                                     | 前端界面源码（React + Vite），多语言文案位于 `web/src/i18n`    |
| `packaging`                               | 应用图标与 macOS `.app` 打包配置                               |
| `docs`                                    | 架构设计文档、已知问题说明及 README 静态配图                   |

非常欢迎提交代码贡献，尤其欢迎在真实的 Mac 设备上进行测试反馈。请参阅 [CONTRIBUTING.md](CONTRIBUTING.md)（英文）；提交 Pull Request 前请先运行 `just lint` 和 `just test`，若条件允许，建议指定一个临时测试 Volume 跑一遍集成测试。

## 许可证

[Apache License 2.0](LICENSE)。

Modal 是 Modal Labs, Inc. 的商标。本项目与 Modal Labs 没有任何关联，也未获得其认可。

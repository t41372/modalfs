<p align="center">
  <img src="packaging/icon.png" width="112" alt="modalfs 图标">
</p>

<h1 align="center">modalfs</h1>

<p align="center">
  <b>把 Modal Volume 挂载成 Mac 上的文件夹。</b><br>
  文件按需流式读取，几 TB 的数据也能马上打开，不占满你的硬盘。
</p>

<p align="center"><a href="README.md">English</a> · 简体中文</p>

<p align="center">
  <img src="docs/screenshots/zh-CN/hero.jpg" alt="名为 datasets 的 Modal Volume 在访达里是一个普通文件夹，里面是 parquet 文件；后面是 modalfs 应用，列出它管理的 Volume：三个已挂载，一个正在上传 1.2 GB">
</p>

---

modalfs 在 macOS 或 Linux 上把 [Modal Volume](https://modal.com/docs/guide/volumes) 挂载成普通文件夹。访达、编辑器、`ffmpeg` 和你的 agent 都像用其他文件夹一样用它，数据则一直在 Modal 上，你在 Modal 上跑的任务读到的也是同一批文件。

```console
$ modalfs volume create datasets
$ modalfs mount datasets                  # → ~/ModalVolumes/datasets
$ echo hi >> ~/ModalVolumes/datasets/notes.md
$ modalfs status
VOLUME    ACCOUNT  STATE    MOUNTPOINT                  PENDING  DOWN/UP
datasets  work     mounted  /Users/me/ModalVolumes/datasets  0        0 B / 3 B
```

> [!IMPORTANT]
> modalfs 是独立项目，**与 Modal Labs 无关，也没有得到其认可**。它用的是 Modal 官方客户端 SDK 所用的同一套 gRPC API，其中一部分没有公开文档，可能随时变化。把重要数据放上去之前，请先读[限制](#限制)。

## 目录

- [特点](#特点)
- [安装](#安装)
- [快速上手](#快速上手)
- [应用](#应用)
- [远程下载](#远程下载)
- [命令行参考](#命令行参考)
- [账号与绑定](#账号与绑定)
- [工作原理](#工作原理)
- [加密 volume](#加密-volume)
- [性能](#性能)
- [限制](#限制)
- [安全](#安全)
- [文件与配置](#文件与配置)
- [常见问题](#常见问题)
- [开发](#开发)
- [许可证](#许可证)

## 特点

**文件马上打开。** modalfs 只流式读取应用实际读到的部分，直接来自 Modal 的块存储。从 100 GiB 文件中间读 4 KiB，只取一个 8 MiB 块；从头读到尾时会并行预读。

**保存时只上传改过的部分。** 改动 100 GiB 文件里的 4 KiB，只上传一个 8 MiB 块。重命名和复制完全不传数据。大文件在后台上传，期间文件夹照常可用。

**你现在用的应用都能直接用。** 它就是普通文件夹，访达、VS Code、Jupyter、`ffmpeg`、Blender 都不需要额外设置。macOS 上不需要内核扩展，由系统自带的 NFS 客户端挂载；Linux 用 FUSE。

**你在 Modal 上的任务看到的是同一批文件。** 挂载同一个 volume 的 Function 能读到你保存的内容，它提交的修改几秒内就会出现在你的文件夹里。

**直接下载到 volume 里。** 粘贴一个链接，Modal 就会用 gopeed、aria2 或 yt-dlp 把它下载到 volume 里，你的 Mac 睡眠时也照样进行，还有实时速度图。这是可选功能，打开之前一直是关着的，因为它是唯一会在 Modal 上运行计算的功能。

**什么都不会丢。** 修改会记在日志里，直到 Modal 收到为止，崩溃和断电也不例外。同一个文件两边都改过时，两个版本都会保留。

**多账号，绝不混淆。** 每个挂载都绑定到自己的账号和 volume ID，所以切换 Modal CLI 的 profile 或给 volume 改名，都不会让挂载指向别处。

**名字由你来取。** 可以给 volume 取一个标签，空格、emoji、任何语言都行。标签存在 volume 里，所以每个用 modalfs 打开它的人看到的都一样，脚本则照旧用 Modal 上的名称。

**需要的话可以端到端加密。** 加密的 volume 里，文件、文件名和标签 Modal 都读不出来。只有它的密钥能打开它，在哪台电脑上都一样。

**一个小巧的原生应用**，一个菜单栏图标，以及能做到应用里所有事情的命令行。

## 安装

### macOS

需要 macOS 13 或更新版本。从 [Releases](https://github.com/t41372/modalfs/releases) 下载 `modalfs-<版本>-macos.zip`，解压后把 **modalfs.app** 拖进“应用程序”。它是同时支持 Apple 芯片和 Intel 的通用应用。如果也想在终端里用命令行：

```sh
sudo ln -sf /Applications/modalfs.app/Contents/Resources/bin/modalfs /usr/local/bin/modalfs
```

应用只做了 ad hoc 签名，没有公证。第一次打开时 macOS 可能会拦下它；这时在访达里右键点按应用，选“打开”。

只要命令行？Releases 里也有 `modalfs-<版本>-darwin-arm64.tar.gz` 和 `-darwin-amd64.tar.gz`。

### Linux

从 [Releases](https://github.com/t41372/modalfs/releases) 下载 `modalfs-<版本>-linux-amd64.tar.gz`（或 `-linux-arm64`），然后：

```sh
tar -xzf modalfs-*-linux-amd64.tar.gz
sudo install modalfs-*-linux-amd64/modalfs /usr/local/bin/modalfs
```

系统需要支持 FUSE（`fuse3` 软件包、`/dev/fuse`）。x86_64 的桌面应用是 `modalfs-gui-<版本>-linux-amd64.tar.gz`，需要 WebKitGTK 4.1（`libwebkit2gtk-4.1-0`）。没有它的话，`modalfs ui` 会在浏览器里打开同样的界面。

### 从源码构建

需要 Go 1.26+、用来构建界面的 [Bun](https://bun.sh) 和 [just](https://github.com/casey/just)（`brew install just`）。macOS 应用还需要 Xcode 命令行工具（`xcode-select --install`），Linux 应用需要 `libgtk-3-dev` 和 `libwebkit2gtk-4.1-dev`。

```sh
git clone https://github.com/t41372/modalfs && cd modalfs
just app          # macOS：dist/modalfs.app，arm64 + x86_64 通用版
just cli          # macOS 和 Linux 的命令行：dist/modalfs-{darwin,linux}-{arm64,amd64}
just linux-gui    # Linux：dist/modalfs-gui
```

在 macOS 12 上，可以用 Go 1.26 和 `MIN_MACOS=12.0 just app` 自己构建应用。这种组合没有测试过，也不提供支持。

`go install github.com/t41372/modalfs/cmd/modalfs@latest` 也能用，但它没法构建界面：这时 `modalfs ui` 会显示一页说明，命令行照常可用。

## 快速上手

1. **添加账号。** 在 [modal.com → Settings → API Tokens](https://modal.com/settings/tokens) 创建一个 token，然后运行：

   ```sh
   modalfs account add                    # 会提示输入 Token ID 和 Secret
   # 或者从 Modal CLI 复制已有的 profile：
   modalfs account import
   ```

2. **挂载一个 volume。** 需要的话可以先创建一个：

   ```sh
   modalfs volume create my-data
   modalfs mount my-data                  # ~/ModalVolumes/my-data
   ```

3. **开始使用。** 在访达或编辑器里打开这个文件夹。文件停止变动约 3 秒后，modalfs 会提交这次修改。

4. 用完后**卸载**。modalfs 会先把还没上传的内容传完。

   ```sh
   modalfs unmount my-data
   ```

modalfs 会记住每个挂载，所以下次 `modalfs mount my-data` 会沿用同一个账号、环境和文件夹。

## 应用

运行 `modalfs ui`，或者打开 **modalfs.app**，会出现一个小窗口：

| 页面 | 可以做什么 |
|---|---|
| **Volume** | modalfs 管理的 Volume，一眼看出每个的状态（已同步、上传中、有冲突、未挂载）、文件夹和大小。从账号里搜索并添加别的 Volume，几百个里也找得到。Volume 的页面显示大小和最大的几个文件夹、还没上传的内容、最近一小时的上传和读取，以及冲突（两个版本都能一键在访达中显示）。挂载、卸载（会先说明还有什么在上传）、更改选项、取标签、从 modalfs 移除、在 Modal 上重命名或删除。 |
| **远程下载** | 添加链接、暂停、继续、删除。正在运行的有几个、总速度、现在每小时花多少；每个下载全程的速度（把光标移到线上可以看那一点的加速度），以及它经历了什么。 |
| **动态** | 挂载、上传、冲突、错误和崩溃恢复，按天分组，重要的会发通知。 |
| **设置** | Modal 账号（连接、检查或更换 token、重命名、默认环境、断开连接）、登录时启动、挂载位置、浅色/深色、语言（English / 繁體中文 / 简体中文）、远程下载，“高级”里还有缓存、预读和连接方式。 |

侧边栏底部的小图标显示是不是全部都在 Modal 上；把光标移上去可以看还有什么在上传。

窗口只是个遥控器。挂载都在后台进程里，所以**关掉窗口不会卸载任何东西**：在 macOS 上 modalfs 会留在菜单栏，它的图标显示是否都已同步到 Modal，菜单里列出你的 Volume（点一下在访达中打开，或挂载它），也能重新打开窗口。**退出**（⌘Q，或菜单栏菜单里的“退出”）则不同：会出现一个清单，先上传剩下的内容，再逐个卸载 Volume，然后退出；还被应用占用的 Volume 可以重试或强制卸载，“立即退出”则不再等待，也不会丢失任何内容。在 macOS 上，窗口是一层透出桌面的玻璃。在浏览器里（没装应用时的 `modalfs ui`）或手机大小的窗口里，同一个界面会把标签移到底部。

<table>
  <tr>
    <td width="50%" valign="top">
      <a href="docs/screenshots/zh-CN/volume.jpg"><img src="docs/screenshots/zh-CN/volume.jpg" alt="深色主题的 Volume 页面：挂载位置、按文件夹的大小、最近一小时的上传和读取，以及挂载选项"></a>
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

给 modalfs 一个链接，你工作区里的一个 Modal Function 就会把它直接下载到 volume 里。这段时间你的电脑可以睡眠、断网甚至关机；文件下载完成后才会出现在你选的文件夹里。

- **打开之前一直是关着的**：在“远程下载”页、设置里，或用 `modalfs download enable` 打开，因为这是 modalfs 里唯一会运行计算的功能。只有你添加或继续一个下载时才会启动 Function，下载完成、暂停或卡住时就停止。
- **费用**：最小规格（0.125 CPU、512 MiB）下载期间每小时约 0.01 美元，另加 Modal 的网络费用（看实时速度会多出约下载量 2% 的流量）。第一次下载，以及 modalfs 更新后的第一次，会用一个短命的 Sandbox 部署 Function：大约 10 秒、0.0001 美元。不需要安装 Python。
- **下载工具**：gopeed、aria2 和 yt-dlp（每次都用最新版）。modalfs 会为每个链接选一个，你也可以自己选。gopeed 在某个服务器上一直失败时，aria2 会自动接手，从头下载那个文件。
- **中断**：Modal 有时会停掉容器来调度资源。下载工具的进度存在 volume 上，所以会在新容器上从原来的地方接着下载，通常一分钟内。一个 Function 最多运行 24 小时，更久的下载会自己换到新的 Function 继续。这些都会显示在下载的页面上，在速度图上标出来，也会用文字说明。
- **实时速度**：在应用里看一个下载时每秒更新四次，由后台进程从容器转发。无论发生什么都留得下来的记录（状态、事件、速度历史）存在 volume 的 `.modalfs/downloads/` 里。
- **Cloudflare WARP**：下载可以经由 Cloudflare WARP，适合会拒绝云服务器的网站。速度会慢一些，而且下载工具除了它什么都连不到：连不上 WARP 时下载会停下，而不是改成直连，你可以选择不经 WARP 重试。
- **下载好的文件从不覆盖任何东西**：名字已被占用时会加上 ` (1)`。
- **费用**：“远程下载”页（以及 `modalfs download spend`）显示工作区和它的下载本月花了多少钱。Modal 的 API 不提供剩余额度和支出上限，所以这两项会链接到 Modal 的 Usage & Billing 页面。Modal 拒绝启动或停止了一个下载时，下载的页面会说明：如果 Modal 说是支出上限或额度的问题，就直接说；否则会说 Modal 没有给出原因，并提示支出上限是其中一个要检查的可能。

```console
$ modalfs download enable
$ modalfs download add datasets:/raw https://example.org/train-2024.tar
$ modalfs download show dl-3f2a --follow
```

远程下载目前还处理得不好的地方见 [docs/known-issues.md](docs/known-issues.md)。

## 命令行参考

```
日常
  modalfs ui [--print]                        打开应用（需要时启动后台进程）；
                                              --print 改为显示浏览器地址
  modalfs status                              各个挂载及其同步状态
  modalfs mount VOLUME [MOUNTPOINT]           挂载并记住它
        [--account A] [--env E] [--ro] [--auto] [--mode auto|nfs|fuse] [--keep-junk]
  modalfs unmount VOLUME|PATH [--now]         提交待上传的写入，然后卸载；
                                              --now 立即卸载，剩下的在后台继续上传
  modalfs sync VOLUME|PATH                    立即提交待上传的写入
  modalfs forget VOLUME|PATH                  删除保存的挂载（volume 本身不受影响）
  modalfs info VOLUME|PATH [--measure]        大小、等待上传的文件、未处理的冲突；
                                              --measure 列出整个 volume 来计算大小
  modalfs conflicts VOLUME|PATH [--handled]   冲突副本；--handled 全部标为已处理

Volumes
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
  modalfs account add [--label L] [--token-id ID] [--env E]
  modalfs account import [PROFILE...]
  modalfs account ls
  modalfs account check ACCOUNT               问 Modal 这个 token 是否仍然有效
  modalfs account token ACCOUNT               更换过期的 token（必须是同一个工作区）
  modalfs account rename ACCOUNT NEWNAME
  modalfs account env ACCOUNT ENVIRONMENT     添加 volume 时在哪个环境里查找
  modalfs account rm ACCOUNT

远程下载（打开之前是关着的；每个下载都在 Modal Function 里运行，向你的账号计费）
  modalfs download enable|disable             打开或关闭远程下载
  modalfs download add VOLUME[:/FOLDER] URL...
        [--downloader gopeed|aria2|yt-dlp] [--size small|medium|large] [--cookies FILE]
        [--warp | --no-warp] [--account A] [--env E]
  modalfs download ls
  modalfs download show ID [--follow]         进度、执行次数和中断、速度
  modalfs download pause ID
  modalfs download resume ID [--warp | --no-warp]
  modalfs download cancel ID [--keep]         停止并删除下载了一半的文件
  modalfs download logs ID                    下载工具的日志
  modalfs download rm ID                      从列表移除（下载好的文件保留）
  modalfs download spend [--account A]        工作区和它的下载本月花了多少钱

后台进程
  modalfs daemon                              在前台运行后台进程
  modalfs quit [--now]                        提交所有内容、卸载、停止后台进程；
                                              --now 立即停止（待上传的写入在下次挂载时上传）
  modalfs service install|uninstall           登录时启动（LaunchAgent / systemd 用户单元）

不经过后台进程（脚本、调试）
  modalfs run VOLUME MOUNTPOINT [flags]       在前台挂载，直到 Ctrl-C
  modalfs serve VOLUME [flags]                只启动 NFS 服务，并打印挂载命令
```

`--account` 可以是名称、账号 ID 或工作区名称。只有一个账号时可以省略。

## 账号与绑定

很多人会在几个 Modal 工作区之间切换。modalfs 的设计保证挂载不会悄悄跟着切换。

- **独立的账号存储。** modalfs 把 token 记在自己的列表里，secret 存在系统钥匙串里。它从不读取 Modal CLI *当前启用*的 profile，只在你运行 `account import` 时读取 `~/.modal.toml`，并复制一份 token。
- **每个挂载都绑定**账号、环境、volume 名称**和 volume ID**。
  - 如果 volume 被**重命名**，modalfs 会按 ID 找到它，更新名称后挂载。
  - 如果 volume 被**删除、又有新的 volume 用了同一个名字**，modalfs 会拒绝挂载，请你重新绑定。它绝不会在旧名称下挂载另一个 volume。

## 工作原理

```
访达 / 编辑器 / agent
  ├─ macOS NFS 客户端 ── 127.0.0.1 上的 NFSv3 (go-nfs) ┐
  └─ Linux FUSE (go-fuse) ─────────────────────────────┤
                                                       ▼
   modalfs 后台进程 ─ vfs：目录缓存（TTL）· 按内容寻址的块缓存（LRU，共享）
                      每个文件的脏块覆盖层 + 日志 · 清单数据库 · 回写
                                                       ▼
   Modal 控制面 (gRPC)：VolumeListFiles2 · GetFile2 · PutFiles2 · CopyFiles2 · RemoveFile2
   Modal 块存储：签名的 GET / PUT URL
```

**Volume v2 按内容寻址。** 一个文件就是一串 8 MiB 块的 SHA-256 哈希。`VolumePutFiles2` 接收这串哈希，只报告服务器还没有的块，而且这种去重跨文件、甚至跨 volume 生效。`VolumeGetFile2` 对任意字节范围，每个块返回一个签名 URL。`VolumeCopyFiles2` 复制清单，例如复制一个 100 GiB 的文件只用了 2.9 秒。

所以局部写入是这样完成的：保留未改动块的哈希，计算改动块的哈希，发送新清单，再上传服务器说缺少的块。

**问题在于：** API 从不返回已有块的哈希。modalfs 通过三种方式得到它们：

1. **本地清单数据库**（bbolt）。modalfs 读到的每个完整块都会计算哈希，按 `(路径, 大小, mtime)` 记录下来。
2. **Sidecar 文件。** modalfs 写入的大文件（≥ 64 MiB）会把块列表存进 volume 里的 `/.modalfs/manifests/<路径>.json`。这样其他 modalfs 客户端，或者缓存刚清空的本机，都能立刻编辑这些文件。只有大小和 mtime 都对得上时才会采信 sidecar。
3. **兜底。** 两者都没有时，例如 Modal 容器刚写好的大文件，第一次局部写入会把没见过的块流式读一遍来算哈希。modalfs 不会把它们存到磁盘上。

其他细节：

- **目录。** Modal 没有真正的空目录，所以 `mkdir` 会写一个隐藏的 `.modalfs_keep` 标记。
- **重命名**在清单层面是复制 + 删除，所以编辑器的原子保存可以正常工作。
- **冲突。** 每次提交前，modalfs 会重新检查远端文件。如果在你编辑期间别人改过它，你的版本会另存为 `name.conflict-YYYYMMDD-HHMMSS.ext`。你也可以把策略改成*覆盖*。
- **macOS 临时文件。** `.DS_Store` 和 `._*` 文件只留在内存里，从不上传。默认开启，可以按挂载关闭。

## 加密 volume

新建的 volume 可以端到端加密：文件、文件名和 volume 的标签在离开你的电脑之前就加密了，所以 Modal 以及其他能读这个 workspace 的人，看到的都只是密文。在你的电脑上，文件夹照常使用。

在应用里新建 volume 时打开 **端到端加密**，或者运行 `modalfs volume create NAME --encrypt`。modalfs 随后会显示这个 volume 的密钥，是一行以 `mfs1-` 开头的文字。**把它存好，比如存进你的密码管理器。** 有了密钥和 Modal 上的文件，在任何电脑上都能读出它们，即使 modalfs 自己的文件都没了也一样。没有密钥，谁都读不出来，modalfs 也不行：没有口令，也没有找回的办法。

- 这台电脑会记住密钥（macOS 上存在钥匙串里；Linux 上存在系统的密码存储区或一个私有文件里），所以 volume 会自动挂载。在别的电脑上它会显示为已锁定：点“解锁”并粘贴密钥，或者运行 `modalfs unlock NAME`。
- Modal Function 读不了加密的 volume，远程下载也不能存进去。
- `modalfs volume decrypt NAME --to FOLDER` 只用密钥就能把所有文件解密复制出来：不需要挂载，也不需要这台电脑记住任何东西。
- Modal 仍然看得到有多少文件和文件夹、目录结构、大小、时间，以及哪些 8 MiB 块内容相同。
- 名字最长 147 个字节（大约 49 个汉字）。重命名一个装了很多文件的文件夹，每个文件都要发一次请求。只有新建的 volume 能加密，volume 的密钥也不能更换。

[格式和每一项取舍](docs/design/encryption.md)都完整写下来了，足以在没有 modalfs 的情况下解密一个 volume。

## 性能

在一个 Linux arm64 沙箱里测得，经过带宽受限的 HTTP/1.1 代理。普通网络下数字会更好。

| 操作 | 结果 |
|---|---|
| 在 100 GiB 文件的 77 GiB 偏移处冷读 4 KiB | 1.45 秒，之后从缓存读 0 毫秒 |
| 顺序读取 512 MiB | 40.9 MiB/s（沙箱把单块 GET 限制在约 7 MB/s） |
| 改动 100 GiB 文件中的 4 KiB | 上传 8 MiB，约 3.5 秒提交 |
| 改动 50 MB 文件中的 4 KiB | 上传 1 个块，2.0 秒 |
| 改动一个从没见过的 20 MB 文件中的 5 字节 | 流式读 2 个块算哈希 + 上传 1 个块，3.7 秒 |
| 复制 / 重命名 100 GiB 文件 | 2.9 秒，不传数据 |
| 写入途中 `kill -9`，然后重启 | 修改被恢复并提交，校验和一致 |

## 限制

- **只支持 Volume v2。** v1 volume 会列出来，但无法挂载。
- **没有锁。** Modal 没有比较并交换（CAS），所以多个写入者写同一个文件时，最后一次提交胜出。modalfs 会尽力检测这种情况，并保留一份冲突副本。
- **8 MiB 粒度。** 小的随机写入也会上传整个 8 MiB 块。
- **元数据。** Modal 在提交时设置 mtime。modalfs 接受 chmod、chown 和 utimes，但会忽略它们。不支持符号链接、硬链接和扩展属性。
- **一致性**是打开时一致（close-to-open），外加 10 秒的目录缓存。其他机器上的改动会在这段时间内出现。Modal 容器需要调用 `volume.reload()` 才能看到你写的内容。
- **回写延迟。** NFS 没有关闭事件，所以文件停止变动约 3 秒后写入才会到达 Modal；持续变动的文件至少每 30 秒提交一次。崩溃后会恢复。一个边缘情况：如果追加写入的最后一秒全是零字节，这些结尾的零可能会丢失。
- **卸载要等上传。** 关闭挂载会先上传待上传的内容，并显示进度；**立即卸载**会马上卸载，剩下的在后台（或下次挂载时）上传。macOS 不会卸载正被应用打开的文件夹，modalfs 会告诉你是哪个应用。
- **挂载期间后台进程必须一直运行**，因为 NFS/FUSE 服务就在它里面。如果它挂了，下次启动时会清理残留的挂载。
- **菜单栏图标只在 macOS 上有。** 在 Linux 上用 `modalfs ui` 重新打开窗口。
- **未公开的 API。** modalfs 依赖官方 SDK 所用的 RPC。如果 Modal 改了它们，modalfs 在更新之前可能无法使用。
- **macOS 的状况。** NFS 服务、后台进程、FUSE 挂载、崩溃恢复、跟随重命名和应用界面都已在 Linux 上测试，macOS 应用可以构建成通用二进制。macOS 上的 NFS 挂载和 macOS 应用还需要更多真实环境的测试，欢迎报告问题。

## 安全

- **Token secret** 存在 macOS 钥匙串或 Linux Secret Service 里。在没有钥匙串的无界面 Linux 上，会退而存进权限为 `0600` 的 `secrets.json`。它们从不写进日志。
- **后台进程监听**一个 Unix 套接字（`0600`，在配置文件夹里）供命令行和应用使用，并在一个随机的 `127.0.0.1` 端口上提供浏览器界面。`modalfs ui` 打开的链接带一个只能用一次的 token，用后换成 cookie；每个请求还需要回环地址的 `Host` 头和一个自定义请求头，以挡住本机的其他网页和 DNS 重绑定。
- **NFS 服务**只监听 `127.0.0.1`，并只接受带你这个用户凭据的请求，所以共用这台 Mac 的其他人挂载不了你的 volume。它防不住什么，见 [docs/known-issues.md](docs/known-issues.md)。
- **破坏性操作**（删除 volume、移除账号）需要明确确认。已挂载的 volume 不能删除。
- 请私下报告安全漏洞（见 [SECURITY.md](SECURITY.md)），不要发在公开的 issue 里。

## 文件与配置

| | macOS | Linux |
|---|---|---|
| 配置、账号、套接字、日志 | `~/Library/Application Support/modalfs/` | `~/.config/modalfs/` |
| 块缓存 | `~/Library/Caches/modalfs/` | `~/.cache/modalfs/` |
| 默认挂载位置 | `~/ModalVolumes/` | `~/ModalVolumes/` |
| 登录时启动 | `~/Library/LaunchAgents/dev.modalfs.daemon.plist` | `~/.config/systemd/user/modalfs.service` |

**缓存大小。** 块缓存只存 Modal 上已经有的数据，所以设成多少都安全：太小只是会重复下载。设置里可以填 1 以上的任意整数 GB，建议至少 2 GB（大约是 2 × 同时读取的文件数 × (预读块数 + 1) × 8 MiB，再加 256 MiB）。还没上传的修改另外存在磁盘上，不计入缓存，所以写一个 100 GiB 的文件，在提交之前需要 100 GiB 的空闲空间。

环境变量：

| 变量 | 作用 |
|---|---|
| `MODALFS_HOME` | 把配置和缓存放到别处（测试时很方便） |
| `MODALFS_TRANSPORT=http1` | 通过 HTTP/1.1 发送 gRPC，用于只允许 HTTP/1.1 的代理 |
| `MODALFS_NO_KEYCHAIN=1` | 把 secret 存进 `secrets.json`，不用钥匙串 |
| `MODALFS_DEBUG=1` | 后台进程输出详细日志 |

## 常见问题

**挂载会花 Modal 的计算费用吗？**
不会。挂载只调用 API。存储和流量照常由 Modal 计费。只有[远程下载](#远程下载)会运行 Function，而它在你打开之前一直是关着的。

**Modal Function 能同时使用同一个 volume 吗？**
可以。在容器里调用 `volume.reload()` 就能看到通过 modalfs 做的改动。避免两边同时写同一个文件。

**为什么 macOS 上用 NFS 而不是 FUSE？**
macFUSE 需要内核扩展，在 Apple 芯片上还得在系统设置里折腾一番。系统自带的 NFS 客户端什么都不用装。如果你已经在用 macFUSE，可以用 `--mode fuse`。

**应用必须一直开着吗？**
不用。只有后台进程需要一直运行。打开**登录时在后台启动**和**启动时自动挂载**，每次重启后 volume 都会在。

**崩溃之后我的修改去哪了？**
modalfs 会在下次启动时重放并提交它。在**动态**里找“找回”那条记录；如果远端文件在此期间被改过，会有一份冲突副本。

## 开发

任务通过 [just](https://github.com/casey/just) 运行，运行 `just` 可以列出全部任务。界面需要 [Bun](https://bun.sh)。

```sh
just build                                   # 本机的命令行/后台进程 → dist/modalfs
just dev-mock                                # 热重载的界面，用示例数据：不需要后台进程和 Modal 账号
just dev                                     # 热重载的界面，连接你正在运行的后台进程
just lint                                    # gofmt、go vet，以及界面的类型检查和翻译检查
just cli                                     # 为所有平台交叉编译命令行/后台进程
just app                                     # macOS 应用（在 Mac 上运行）
just test-e2e                                # 真实的 NFS 挂载，连接网速受限的假 Modal（macOS）
just fake -up 20M                            # 假的 Modal，不用账号就能试 modalfs
MODALFS_TEST_VOLUME=scratch just test-integration   # 会在这个 volume 里创建和删除文件
```

界面用 React、Tailwind 和基于 Base UI 的 shadcn/ui 写成，风格与 [Jezo](https://github.com/t41372/Jezo) 一致。`just ui` 把它构建到 `internal/webui/dist`，由后台进程和应用嵌入。本文的图片来自 `cd web && bun run screenshots`：它用 `web/src/api/mock.ts` 里的示例数据（平静的 `?scenario=demo`）构建界面，按应用自己的窗口大小、每种 README 语言拍下每一页，再把透明的窗口叠在 `docs/screenshots/backdrops/` 里的画上（画来自 Jezo）。第一张图另外放了一个真实的访达窗口，由 `web/scripts/capture-finder.sh` 从挂载的假 Modal 上截取。

目录结构：

```
cmd/modalfs        命令行 + 后台进程入口（纯 Go）
cmd/modalfs-gui    桌面应用（Wails v2），也可以运行后台进程
internal/modalapi  Modal gRPC 客户端、块上传/下载、volume 管理
internal/vfs       文件系统核心：缓存、脏块覆盖层、日志、冲突
internal/nfsfront  NFSv3 前端（go-nfs）
internal/fusefront FUSE 前端（go-fuse）
internal/daemon    挂载管理、JSON API、登录时启动
internal/config    账号、保存的挂载、设置、secret
internal/webui     为后台进程和应用嵌入构建好的界面
web                界面源码（React、Vite）；文案在 web/src/i18n
packaging          图标、macOS 应用打包脚本（`just app`）
docs/screenshots   README 的图片，由 web/scripts/screenshots.ts 生成
```

欢迎贡献。特别需要在真实 Mac 上的测试：NFS 挂载、应用、钥匙串和 LaunchAgent。环境搭建见 [CONTRIBUTING.md](CONTRIBUTING.md)（英文）。提交 PR 前请运行 `just lint`，可以的话再用一个临时 volume 跑一遍集成测试。

## 许可证

[Apache License 2.0](LICENSE)。

Modal 是 Modal Labs, Inc. 的商标。本项目与 Modal Labs 无关。

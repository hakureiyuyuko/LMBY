# 转码（M4）

这份文档记三件事：**这台机器能干什么**（实测，不是"ffmpeg 里有这个编码器"）、
**参数怎么拼**（照 Jellyfin 参考代码的配方，见 `docs/notes/jellyfin-reference.md`）、
**怎么验证**。

---

## 一、本机能力矩阵（2026-09-21 实测）

> ⚠️ **下面这张表是「某一次在某台机器上」的实例测量值，不是项目的常量。**
> LMBY 自己不知道也不写死你是 Intel 还是 AMD、有没有显卡、ffmpeg 在哪个路径 ——
> 这些全部由运行时的**真跑探测**得出（见 `internal/encoder`）：静态层读
> `-version/-hwaccels/-encoders/-filters` 与设备节点；真跑层用 lavfi 生成 1 秒小样，
> **逐个后端、逐个码率模式真编一遍**，再用真文件测硬件解码。只有真跑通过的后端
> 才会被使用。所以换一台机器（甚至换一版驱动）结果就会不同。
>
> 也正因为如此，探测**不会**只试一种码率模式：老 Intel i965 只吃 CQP、
> 新 iHD 与 AMD 的 VAAPI 更习惯 VBR、QSV 常用 ICQ、NVENC 用 CQ ——
> 只拿一种当真跑判据，到了别的机器上就会把**本来可用的后端误判成不可用**。
> 后端偏好与设备节点都可以在配置里覆盖：
> ```toml
> [playback]
> encoder = "auto"                 # 或 vaapi / qsv / nvenc / videotoolbox / amf / software
> # vaapi_device = "/dev/dri/renderD128"
> ```

宿主：PVE 上的 LXC `lmby-dev`（Intel **i5-10500T**，4 核 8 线程 2.3GHz，`/dev/dri` 直通）
ffmpeg 7.1.5，VAAPI 驱动 **Intel iHD 25.2.3**。

### 编码

| 后端 | 编码器 | 结果 | 1080p 倍速（实测） |
|---|---|---|---|
| **VAAPI** | `h264_vaapi` | ✅ 可用 | **11.8x**（CQP23）/ 9.7x（CBR 8M） |
| VAAPI | `h264_vaapi` @720p | ✅ | **17.3x** |
| VAAPI | `hevc_vaapi` | ✅ 可用 | 3.75x（CQP26） |
| VAAPI | `av1_vaapi` | ✅ 在列表里（本机 iGPU 无 AV1 编码，真跑会失败，探测会标不可用） | — |
| CPU | `libx264 veryfast` | ✅ | **≈0.9x**（**不够实时！**） |
| CPU | `libx265 ultrafast` | ✅ | ≈1.4x |
| QSV | `h264_qsv` | ❌ 编码器在列表里，**真跑报 -22** | — |
| NVENC | `h264_nvenc` | ❌ 没有 NVIDIA 卡 | — |

> **结论：这台机器上硬件转码不是优化项，而是唯一可行路径。**
> 4 核低功耗 CPU 上的 `libx264 veryfast` 只有 0.9x 实时 —— 纯 CPU 转码必然卡。
> VAAPI 硬解硬编 11.8x，余量很足（还能同时跑好几路）。

### 解码（硬解）

| 输入 | 结果 |
|---|---|
| H.264 1080p 8bit | ✅ `-hwaccel vaapi -hwaccel_output_format vaapi` |
| HEVC 1080p **10bit** | ✅ 可用（我们库里最主流的格式） |
| H.264 **High 10（Hi10P）** / YUV422P10 | ❌ **解不了**：`Failed setup for format vaapi: hwaccel initialisation returned error`。能力探测只能验到「h264 能不能硬解」这一层，验不到同一个编码的各个变体 —— 所以服务端起播失败时会**自动降级成软件解码 + 同一个硬件编码器**（`api.readySession`，见下面「踩到的坑」） |

**硬解起不来时自动降级**：能力探测只能验到「编码名」这一层，验不到同一个编码的各个
变体（Hi10P 就是反例）。所以真正的判据是**起播那次真跑**：开了硬解的会话如果 20 秒内
产不出第一个分片，服务端会自动换成软件解码 + **同一个硬件编码器**重试一次
（`api.readySession`）—— 编码才是吃 CPU 的大头，解码那点开销换「能看」完全值得。
只换解码不换编码，所以输出画质/码率与原来一致。库里 11 个 Hi10P 文件全靠这条降级才播得出来。

### 滤镜

| 滤镜 | 结果 | 用途 |
|---|---|---|
| `scale_vaapi` | ✅ | 硬解硬编路径上的缩放（不下载到内存） |
| `deinterlace_vaapi` | ✅ | 硬解去隔行（本库无隔行内容，留作能力） |
| `tonemap_vaapi` | ❌ **实测用不了** | 存在，但 iHD 驱动要求输入带 mastering display 元数据；库里常见的 bt2020+PQ 素材没有那段数据，会直接报 `No mastering display data from input` 并让**整路转码起不来**。所以 HDR 一律走软件链 |
| `tonemap` + `zscale`（软件） | ✅ 实测可用 | HDR→SDR 的唯一可行路径（硬解时配 `hwdownload`） |
| `yadif` / `bwdif` | ✅ | 软件去隔行 |
| `subtitles`（libass） | ✅ | 字幕烧录 |

其他实测细节：
- `-low_power 1` 在 iHD 25.2.3 上**可用**（早期笔记说会报错，那是旧驱动的结论，已作废）
- VAAPI 的 `CQP` 与 `CBR` 两种 `-rc_mode` 都能用（iHD 驱动不像老的 i965 只吃 CQP）

### 我们的库（472 条目 / 479 文件）

| 项目 | 数量 | 说明 |
|---|---|---|
| **需要视频转码** | **387 / 479** | 非 h264/vp9/av1 或 10bit → M4 解锁 80% 的库 |
| HDR10（`smpte2084` + bt2020） | 13 | 需要 tone mapping |
| 隔行内容 | 0 | 不用为它写分支，但保留能力 |
| 音频 | flac 2ch ×197、aac ×106、dts 6ch ×63、eac3 6ch ×27、ac3 6ch ×21、flac 6ch ×19、dts 8ch ×5 | 浏览器只认 aac/mp3/opus；其余都要转 AAC（M3 已实现「复制 / 转 AAC + 降混」） |

---

## 二、参数配方

### 转封装（M3，已上线）

```
-ss <起播秒> -i <源> -map 0:<视频绝对序号> -map 0:<音频绝对序号>
-c:v copy -c:a copy 或 -c:a aac -ac 2 -b:a 192k
-sn -dn -t <窗口秒>
-f hls -hls_time 4 -hls_playlist_type event -hls_list_size 0
-hls_segment_type fmp4 -hls_flags independent_segments+temp_file
-hls_fmp4_init_filename init.mp4 -hls_segment_options movflags=+frag_discont+skip_sidx
-map_metadata -1 -map_chapters -1 -max_delay 5000000
```

### 转码（M4）

在转封装的基础上换掉视频那一段（**其余保持不变，走同一套会话/分片/回收**）：

```
# 硬件路径（本机首选）
-vaapi_device /dev/dri/renderD128
-hwaccel vaapi -hwaccel_output_format vaapi
-vf scale_vaapi=w=<宽>:h=<高>:format=nv12        # 需要缩放时
-c:v h264_vaapi -rc_mode CQP -qp <档位>          # 实测可用；CBR 亦可
-force_key_frames:v 'expr:gte(t,n_forced*<分片秒>)'

# 软件兜底
-c:v libx264 -preset veryfast -crf <档位> -sc_threshold:v 0
-force_key_frames:v 'expr:gte(t,n_forced*<分片秒>)'

# HDR→SDR（仅 source 是 HDR 时）：走**软件**色调查映射，+
# 硬解时先在显存里缩放再取回内存（见下面「实测踩到的两个坑」）
-vf "scale_vaapi=w=<宽>:h=<高>,hwdownload,format=p010,zscale=t=linear:npl=100,tonemap=hable,zscale=p=bt709:t=bt709:m=bt709:r=tv,format=nv12,hwupload"
```

实测踩到的两个坑（都是**真跑**才发现的，参数看起来都对）：

1. **`tonemap_vaapi` 会整路失败**：报 `No mastering display data from input` 后滤镜链
   直接 `Invalid argument`，用户看到的是「放不了」。而 lavfi 探测造不出这种素材，
   所以能力探测答不了这个问题 —— 解决办法是 HDR 一律走软件链（zscale + tonemap）。
   能力探测里的 `tonemap` 因此改成只认「zscale + tonemap 两个软件滤镜在不在」。
2. **缩放不能再放在 tonemap 之后**：先在显存里 `scale_vaapi` 到目标分辨率，
   再 `hwdownload` 交给 CPU 色调映射，速度差 4 倍（实测 4K@60fps 源：
   不缩放 0.10x → 先缩到 1080p 0.39x）。4 秒分片的起播耗时从 40 秒降到 10 秒，
   这才进得了 20 秒的起播超时。

### 转码输出的幅面上限（`[playback] transcode_max_height`，默认 1080）

转码是「边编边播」，**输出分辨率直接决定能不能实时**。所以转码路径额外压一档：
默认不超过 1080p（`0` = 不限）。

- 只约束**转码**：直出与转封装不重编码，源多大就送多大（4K 原样送又快又清楚）。
- 判定顺序：`min(源幅面, 客户端上报的上限, transcode_max_height)`，理由链会写出实际缩到多少。
- 依据是**实例测量值**，不是项目常量：这台机器上 4K HDR 做色调映射只有 0.10x，
  压到 1080p（并在显存里先缩放）后 0.39x；而 1080p 的 HEVC 10bit 转码起播实测只要 1 秒。
  机器够强、或就是要 4K 输出，把它调大或写 0。

### 播放器画质档位（用户自己选）

播放器控制条上的「画质」菜单，接口字段是 `maxHeight`（三态）：

| 菜单选择 | 请求里的 maxHeight | 行为 |
|---|---|---|
| 自动（默认） | 省略 | 转码时按配置的 `transcode_max_height` 压 |
| 原生（源分辨率） | `0` | 不额外压；4K 转码可能起播慢，这是用户自己选的 |
| 1080p / 720p / … | 数字 | 输出高度上限 |

菜单只列**比源低**的档（等于源高度的用「原生」表示，比源高的档没有意义）。
几条容易踩空的规则：

- **选了比源低的档就必须转码**，哪怕源本来能直出 —— 否则就是「选了画质却没变化」。
  理由链写成「你选了 720p 输出（源是 2160p）」，让人知道是自己选的，不是服务端不给原画质。
- **选了比源高的档等于没选**（不转码）：转码放大没有任何收益。
- **机器编不了时不把能看的片子变成「放不了」**：回退成原样复制，并在理由里说明。
- ⚠️ 会话键 `streamKey` 里必须包含**编码参数指纹**：条目、起播点、模式都可能没变，
  但画质档一变 ffmpeg 命令行就完全不同 —— 键相同会复用上一档的会话，用户切了档位
  画面却不变（真跑踩到过）。

关键点（为什么这么写，见 `docs/notes/jellyfin-reference.md` 的 `文件:行`）：
- **关键帧必须对齐分片边界**，否则分片长度漂、播放器起播慢、seek 不准
- libx264 还要 `-sc_threshold 0`，不然场景切换会插关键帧
- HDR 素材不能直接丢给 h264：颜色会发灰发暗，必须 tone mapping
- **10bit 源不缩放也要降位深**：目标 h264 只接受 nv12，帧格式不匹配时编码器
  会在初始化阶段直接失败（表现为「等待第一个分片超时」而不是画质问题）
- **不用 `-re`**（那会等速读源，白等）；节流靠下面第三节

### 节流（M4）

转码比实时快得多（本机 1080p 实测 20x+），不节流的话用户刚点开几秒、CPU 就把整个
300 秒窗口编完了。所以：**已生成位置 − 客户端消费到的位置 > 阈值**（默认 60 秒）
就让 ffmpeg 歇着，等客户端追上来再继续（开关与阈值见 `[playback] throttle_seconds`）。

⚠️ **用的是 `SIGSTOP` / `SIGCONT`，不是 Jellyfin 那套「往 stdin 写 `p`/`u`」**：
实测 Debian 的 ffmpeg 7.1.5 **在 stdin 是管道时根本不处理按键**（显式加 `-stdin`
也一样，帧数一路涨）。SIGSTOP 唯一的顾虑是「暂停时留下半写分片」，而这一点我们已经用
`-hls_flags temp_file`（分片先写临时文件、写完才改名）堵住了。

其余要点：

- 「客户端消费到哪」取两个来源的**较大值**：分片请求（拉到哪个分片）与进度上报
  （真实播放位置）。取小会导致「刚恢复又暂停」来回抖。
- 恢复阈值取「提前量的一半」，避开在阀值上反复横跳。
- **取证方式**：暂停后 ffmpeg 的 `/proc/<pid>/status` 里 `State` 是 `T`（stopped）——
  验收脚本靠这个确认「真的停了」，而不是我们单方面记了个状态位；
  会话状态接口（`/api/v1/playback/sessions`）里也能直接看到
  `generatedSeconds` / `clientSeconds` / `aheadSeconds` / `throttled`。
- Windows 上没法暂停进程，节流退化为「不节流」（记一次日志，不影响播放）。

⚠️ **客户端位置要从窗口起点起算**：`已生成` 是**绝对**媒体位置（窗口起点 + 分片数×分片时长），
而客户端位置在起播瞬间还是空的（既没拉分片，也没上报进度）。所以会话一建好就把
「窗口起点」垫成客户端位置 —— 不这么做的话，从影片中途续播（比如 20 分钟处）会一算就
得出「领先 1200s」，**起播瞬间就把 ffmpeg 停住**，20 秒产不出第一个分片，整路被判成「放不了」。
真跑踩到过：从 23:23 续播一部片子，直接开不起来。
（`internal/stream/throttle_test.go` 的 `TestThrottleMidMovieResume` + 验收脚本第 12.5 节）

---

## 四、字幕

三种形态，按「能不能还原原意」分：

| 字幕 | 走法 | 为什么 |
|---|---|---|
| **ASS/SSA** | **原样抽出（`-c:s copy`）→ 前端 libass（WASM）渲染** | 定位（`\pos`）、轨迹（`\move`）、插值动画（`\t`）、卡拉OK（`\k`）、矢量绘图（`\p`）——只有完整的 ASS 解释器能还原 |
| SRT 等纯文本 | 转 WebVTT 走浏览器原生轨道 | 轻、起播快；纯对白字幕不需要特效 |
| 图形字幕（PGS/VobSub） | **`overlay` 烧进画面**（用户选「烧进画面」时才做，必须重编码） | 位图，前端渲染不了也转不成文本 |

**ASS 那条链路**（我们库里 145 个文件带 ASS）：

- 服务端：`GET /api/v1/play/{sid}/subtitles/{index}.ass` —— 内嵌字幕用
  `-map 0:N -c:s copy -f ass` **原样**抽出（落盘缓存，与 WebVTT 共用单飞与 202 轮询逻辑）。
  决策层在 `StreamPlan.DeliverAs` 里标 `libass`，播放器据此选渲染方式（`webvtt` 走 `<track>`）。
- 前端：懒加载 `SubtitlesOctopus`（libass 的 WASM 版，MIT；资源在 `/subtitles-octopus/`，
  构建时从 npm 包 `libass-wasm` 复制，见 `web/scripts/copy-octopus.mjs`）。
  只有真的要看 ASS 字幕才拉（~1.5MB，浏览器会缓存）。
  ⚠️ `libass-wasm` 是 **LGPL-2.1-or-later**（含 FFmpeg 组件）：以独立文件形式提供、
  并保留它的 `COPYRIGHT`，即满足「可替换」的要求。
- 两条渲染路互不干扰：WebVTT 用 `<track>`，libass 是盖在画面上的 canvas
  （`pointer-events: none`，不吃鼠标事件）。

⚠️ **这一节有四个真坑，都是实盘踩出来的**（前三不解决就是「一片空白、控制台只有一句话」）：

1. **兑底字体必须由服务端提供**。libass(WASM) 只认它自己虚拟文件系统里的字体，看不到
   客户端的系统字体；而 octopus 默认要的 `default.woff2` 在 npm 包里**根本不存在** ——
   缺了它 worker 会 fetch 失败并**直接崩掉**（控制台只有一句 `Worker error: ErrorEvent`）。
   部署时把一个中文字体放到 `<数据目录>/fonts/fallback.ttf`，前端通过
   `/api/v1/fonts/fallback.ttf` 取（接口有登录校验，且 `filepath.Base` 挡路径穿越）。
   **字体**：**Noto Sans CJK**（`NotoSansCJK-Regular.ttc`，19MB，SIL OFL 1.1，一个文件覆盖简/繁/日/韩）；安装：
   `apt-get install -y fonts-noto-cjk`，把 `/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc`
   拷成 `<数据目录>/fonts/fallback.ttc`。许可演进：文泉驿（GPL 系，嵌入有争议）→
   阿里普惠体（官方只有「免费商用」声明，不是标准开源协议，包里没有任何许可文本）→
   **Noto / 思源（SIL OFL 1.1，明确允许嵌入、再分发与商用）**。
   字体文件**不入库**（几 MB 的二进制），部署时自己放。
2. **资源必须用绝对 URL**。SPA 路由下（`/play/123`）相对路径会被解析成 `/play/xxx.js`，
   静态服务只会回 404 —— 渲染器连 worker 都起不来。
3. **要等视频有真实尺寸**再建渲染器。octopus 按 `setVideo` **那一刻**的尺寸建画布，
   拿到 0 就把画布 `display:none`，而且之后不会自己重算。
4. **字幕文本由我们自己取回再喂进去**（`subContent`）。内嵌字幕首次要抽（接口回 202），
   让 worker 自己去拉会拿到 202 里的 JSON 然后崩；自己取回还能把失败原因告诉用户。
5. **时间基准要对齐**（用户报的「播一会就对不上」）。
6. **自己盯视频尺寸变化**。octopus 只在创建时和 window resize 时算画布尺寸，
   **视频自己**的尺寸变化（续段换窗口、切画质、进全屏）它不管 —— 更糟的是尺寸
   瞬间为 0 时它会把画布 `display:none` 且不恢复（表现：续段之后字幕直接没了）。
   用 `ResizeObserver` 盯住 video，尺寸回来就调 `resize()`。我们的 HLS 是**窗口相对时间**
   （`video.currentTime` 从本窗口算起），而字幕时间轴是**片源绝对时间**。窗口一换
   （续段/拖动），差值就变了。octopus 支持 `timeOffset`（它算的是
   `video.currentTime + timeOffset`）—— 创建时传当前窗口起点，窗口一变就同步。
   否则表现就是：开头准、播一会儿越来越偏。

实测（本机）：ASS 原文 3.97MB / 274 行 Dialogue 抽取与渲染正常，带定位与装饰的日文
标题字幕能在浏览器里正确显示（截图见 `shots-play/07-libass.png`）。

### 图形字幕烧录（M4，2026-09-21）

图形字幕（PGS/VobSub）是**位图**：浏览器渲染不了，也转不成文本 —— 唯一的看法就是
把它叠进画面。代价是必须重新编码，所以决策层会把这个文件**强制拉进转码**
（即使它本来能直出），理由链里写「按你的选择烧录字幕」。因此它是**用户显式选择**
（播放器的字幕菜单里标「图形，需烧录」），不是默认行为。

**滤镜图**（`internal/encoder/args.go` 的 `assembleVideoArgs`）：

```
[0:<字幕绝对序号>]scale=w=<W>:h=<H>:force_original_aspect_ratio=decrease:flags=bilinear,
                  pad=w=<W>:h=<H>:x=(ow-iw)/2:y=(oh-ih)/2:color=black@0,format=yuva420p[sub];
[0:<视频绝对序号>]<主链>[main];
[main][sub]overlay=eof_action=pass:repeatlast=0,<收尾>[vout]
```

- 位图必须**缩放到输出尺寸**：位图的尺寸是**片源**的（库里带 PGS 的文件基本都是
  1920×1080 的位图），画质档把画面缩到 720p 之后不跟着缩就会溢出画面；
- `color=black@0` 是**全透明**黑 —— 位图带 alpha，不透明黑会在画面糊一条边；
- `eof_action=pass`：字幕轨比视频短时（只有前半段有字幕）放行主输入，不让画面提前结束；
- `-map [vout]` 取代了平常的 `-map 0:<视频序号>`。

**为什么 overlay 只能在内存里做**：`overlay` 是软件滤镜。硬解时帧在显存里，
所以主链末尾要 `hwdownload,format=nv12` 取回内存，叠完再 `format=nv12,hwupload` 传回去。
软解时帧本来就在内存里，不需要这两步。

**实测（本机 i5-10500T / iHD，PGS 测试片 1080p HEVC Main10）**：

| 路径 | 速度 |
|---|---|
| VAAPI 硬解 + 烧录，输出原生 1080p | **2.94x** |
| VAAPI 硬解 + 烧录，输出 720p | **4.11x** |
| 同条件不烧录（软解 + 硬编） | 40x+ |

⚠️ 这些是**实例测量值，不是项目常量**：换机器/驱动就会变。烧录比不烧录贵一个数量级，
这就是它做成显式选择的原因。

**踩到并修掉的坑（都是真跑出来的）**：

1. **只能 map 滤镜输出的标签，绝不能同时 `-map 0:<视频序号>`**。同一路视频被送两遍，
   ffmpeg 在滤镜协商阶段直接失败（`Impossible to convert between the formats
   supported by…`）—— 表现是「放不了」，不是画面差一点。
2. **软件帧上不能用硬件滤镜**。`scale_vaapi` / `deinterlace_vaapi` 作用在软解出来的帧上
   **必然失败**（ffmpeg 不会替你补 hwupload）。原实现在「不硬解 + VAAPI 硬编」时就是
   这么写的（`-vf scale_vaapi=…` 而没有上传），现在改成软件链
   （`scale=…,format=nv12,hwupload`）—— 顺手修掉的既有 bug。
3. **`overlay_vaapi` 在本机 iHD 上不可用**（编码器初始化就失败，与 overlay 无关），
   所以走的是通用性最好的「取回内存 → 软件叠加 → 传回显存」，而不是硬件叠加。
4. **决策层要问「字幕是什么」而不是「用户点没点」**：选了烧录但选中的是**文本**字幕时
   不该重编（它走 WebVTT / libass 旁路）。这个顺序在被单测拓到之前是反的。

验收：`scripts/dev/verify-transcode.sh` 第 15 节做**开/关对照** —— 同一时刻取一帧，
烧录 vs 不烧录做像素差（`blend=difference` 的 YMAX）：有字幕的时刻差得很大
（实例 208），没字幕的时刻只差管线噪声（实例 13）。另外还直接看**跑着那个 ffmpeg
的命令行**，确认 `-filter_complex` / `[0:<字幕序号>]` / `-map [vout]` 真的在里面。

实测：PGS 字幕「让您久等了 / お待たせしました」能正确烧进画面（截图见
`docs/images/player-burn-pgs.png`）。

**字幕这条线还没做的**：图形字幕烧录（`subtitles=f='…':sub2video=1:fontsdir='…'`）、
ASS 附件字体（`\fn` 引用的字体常以附件形式存在）、外挂字幕的 GB18030 编码探测。

---

## 五、怎么验证

```bash
# 1. 能力表（真跑探测）——接口是登录即可读
curl -s -b <cookie> http://<host>:8099/api/v1/transcode/capabilities | jq '.best, .capabilities.warnings'
curl -s -b <cookie> -X POST http://<host>:8099/api/v1/transcode/capabilities/refresh   # 管理员：装完驱动后手动刷

# 2. 离线脚本：把探测结果和「这台机器实际能干什么」对一遍
bash scripts/dev/verify-caps.sh

# 3. 转码端到端（真转真播，含倍速与进程回收）
bash scripts/dev/verify-transcode.sh
```

能力表的探测耗时**实测约 6 秒**（其中大头是 QSV/NVENC 两个不可用后端各自的失败等待）——
所以它只在启动时后台跑一次并落盘缓存，不拖慢启动，也不在播放路径上现等。
缓存在 `<数据目录>/capabilities.json`（有效期 24 小时，也可手动刷新）。
它跟接口返回的格式完全一致 —— 排查问题时 `cat` 一下就够了。

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

不用 `SIGSTOP`，而是**往 ffmpeg 的 stdin 写 `p`（暂停）/ `u`（恢复）**——
这是 Jellyfin 的做法（`TranscodingThrottler.cs`），比发信号温和：ffmpeg 会正常
收尾当前分片并 flush 索引，在网络盘上不会留下半写状态。

判定依据：**已生成位置 − 客户端最近请求到的分片位置 > 阈值（默认 60 秒）** 就暂停，
差值回落再恢复。注意命令行**不能带 `-nostdin`**，否则按键通道不存在。

---

## 三、怎么验证

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

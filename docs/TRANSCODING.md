# 转码（M4）

这份文档记三件事：**这台机器能干什么**（实测，不是"ffmpeg 里有这个编码器"）、
**参数怎么拼**（照 Jellyfin 参考代码的配方，见 `docs/notes/jellyfin-reference.md`）、
**怎么验证**。

---

## 一、本机能力矩阵（2026-09-21 实测）

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
| `tonemap_vaapi` | ⚠️ 存在，**未用真实 HDR 素材验证过** | HDR→SDR |
| `tonemap` + `zscale`（软件） | ✅ 存在 | HDR→SDR 的软件兜底 |
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

# HDR→SDR（仅 source 是 HDR 时）
-vf ...,tonemap_vaapi=format=nv12                 # 硬件
-vf ...,zscale=t=linear:npl=100,tonemap=hable,zscale=p=bt709:t=bt709:m=bt709:r=tv,format=yuv420p  # 软件
```

关键点（为什么这么写，见 `docs/notes/jellyfin-reference.md` 的 `文件:行`）：
- **关键帧必须对齐分片边界**，否则分片长度漂、播放器起播慢、seek 不准
- libx264 还要 `-sc_threshold 0`，不然场景切换会插关键帧
- HDR 素材不能直接丢给 h264：颜色会发灰发暗，必须 tone mapping
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

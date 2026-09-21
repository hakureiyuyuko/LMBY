# Jellyfin 参考代码：M4（转码 / 探测 / 字幕）要点

参考代码位置（只读浅克隆，**不要复制代码**）：
`C:\Users\admin\Desktop\dev\_ref\jellyfin`

> ⚠️ 边界：Jellyfin 是 **GPL-2.0**，LMBY 是 **AGPL-3.0**，两者不兼容 ——
> 这里只**读实现思路、抄参数配方、对语义**，代码全部自己写。
> 每次引用都在下面标注 `文件:行`，方便回去核对原文。

---

## 一、转码节流：写 stdin，不是 SIGSTOP

**结论**：Jellyfin 不向 ffmpeg 发信号，而是**往它的 stdin 写按键**。

`MediaBrowser.MediaEncoding/Encoder/TranscodingThrottler.cs`

| 要点 | 做法 |
|---|---|
| 暂停 / 恢复 | `process.StandardInput.WriteAsync("p")` / `"u"` |
| 兼容老版本 | 若不支持 `p` 键：`"c"` + 换行（走 sendcmd） |
| 判定周期 | 每 5 秒检查一次 |
| 判定条件 | `转码位置 - 客户端已下载位置 > ThrottleDelaySeconds`（默认 300，最小 60）才暂停；差值小于它就给客户端时间追上 |
| 硬性前提 | **ffmpeg 命令行不能带 `-nostdin`**，否则按键通道根本不存在 |
| 支持性探测 | 先看 ffmpeg 构建里有没有 `p` 键支持（`IsPkeyPauseSupported`） |

LMBY 的现状与计划：M3 的分片参数里带了 `-nostdin`（防它抢终端），M4 要改成
「保持 stdin 打开 + 按键节流」，并把判定依据从「窗口」升级成
「已生成位置 vs 客户端最近请求的分片位置」。
好处：比 SIGSTOP 温和（ffmpeg 会正常收尾当前分片、flush 索引），也不会在
CIFS 上留下半写状态的分片。

---

## 二、HLS 输出参数（我们漏了两条，一条影响体验、一条影响正确性）

`Jellyfin.Api/Controllers/DynamicHlsController.cs:1594` 起

```
-hls_playlist_type event          # 我们已有：只追加、不重写
-hls_list_size 0                  # ★ 我们没设 → 默认 5！
-hls_time <3 或按码率算>
-avoid_negative_ts disabled
-copyts                           # ★ 保留源时间戳（绝对时间轴）
-start_at_zero                    # 仅在需要把时间轴拉回 0 时用
-max_delay 5000000                # 网络源读写的缓冲上限
-frag_discont                     # fMP4：跨不连续点（音视频起步不同）也照样播
-hls_segment_options movflags=+frag_discont+skip_sidx
-map_metadata -1 -map_chapters -1 # 丢掉源文件的章节/元数据
-hls_base_url hls/{name}/         # 分片引用用绝对前缀（我们是相对路径，等价）
```

**`-hls_list_size 0` 是必须补的**：默认只有 5 片 → 播放列表只保留最近
约 20 秒，客户端能就地跳的区间也就只有 20 秒。这就是 LMBY 里「拖到窗口内
靠后的位置也得让服务端重开一段 ffmpeg」的直接原因。
（M3 的客户端已经能做到「只有确实在已加载区间内才本地跳」，补上这条之后
窗口内拖动 0～300 秒都能瞬时响应。）

**`-copyts` 值得跟进**：保留源时间戳后，分片 PTS 就是**绝对媒体时间**
（seek 到 943s 的分片 PTS 从 943 起），客户端不需要再自己维护
「窗口起点 + currentTime」的偏移映射 —— 我们 M3 那套 `baseRef` 簿记可以整个删掉。
Jellyfin 是用 `-copyts -avoid_negative_ts disabled` 组合做的，HLS 里配
`EXT-X-START`/事件播放列表一起用。**这条放到 M4 一起改**（改它要动 M3 已验证的
客户端映射，单独改风险不值当）。

---

## 三、转码的关键帧对齐（不这么做，分片边界就是烂的）

`MediaBrowser.Controller/MediaEncoding/EncodingHelper.cs:2014`

- 默认（**VAAPI 走这条**）：`-force_key_frames:0 "expr:gte(t,n_forced*<分片秒数>)"`
- libx264/libx265：再加 `-sc_threshold:v:0 0`（关掉场景切换关键帧，否则分片长度漂）
- QSV/NVENC/AMF/RKMPP/SVT-AV1 不认强制关键帧：改用
  `-g:v:0`、`-keyint_min:v:0` = `ceil(分片秒数 × 帧率)`
- AMD 的 hevc_vaapi 要额外 `-flags:v -global_header -bsf:v extract_extradata=remove=0`

---

## 四、VAAPI（我们这台机器的约束最紧）

`EncodingHelper.cs:1698`（码率模式）、`:286`（滤镜支持性探测）、`:5270` 起（滤镜链）

| 项 | Jellyfin 的做法 | 我们这台实测 |
|---|---|---|
| 设备初始化 | `-vaapi_device /dev/dri/renderD128`（容器里就是 `/dev/dri` 直通进来的那个） | 同 |
| 解码 | `-hwaccel vaapi -hwaccel_output_format vaapi`（不支持时退回软解 + `hwupload`） | 可用 |
| 滤镜链 | `format=nv12\|vaapi,hwupload` / `scale_vaapi=w=..:h=..` / `format=nv12\|vaapi,hwupload=derive_device=vaapi` | 不指定时默认 device 0 |
| 码率模式 | Intel i965 驱动用 `-rc_mode CBR -b:v .. -maxrate .. -bufsize ..`；其它驱动用 VBR | **只吃 CQP**：`-rc_mode CQP -qp N`；`-low_power 1` 直接报错 |
| 能力探测 | 看 encoder 列表里的 `h264_vaapi/hevc_vaapi`，以及滤镜列表里的 `scale_vaapi` | 已确认可用 |

**M4 的落地顺序**：先探测（真跑一小段 + 看 `-encoders`/`-filters`/`-hwaccels` 输出），
再按「VAAPI → 软编」两级降级；质量档位在 i965 上映射到 `-qp`（CQP），
不要照抄 CBR 那套。

---

## 五、字幕

### 5.1 烧录（图形字幕 / 用户选择烧录时）

`EncodingHelper.cs:1964`

```
-vf subtitles=f='<字幕文件路径>':alpha=1:sub2video=1:fontsdir='<字体目录>'
```

- **先抽成外挂文件再烧**：内嵌字幕要先 `-map 0:s:N -c:s ass/srt` 导出一份，
  然后用文件路径去烧（Jellyfin 走 `_subtitleEncoder.GetSubtitleFilePath`）
- `sub2video=1`：图形字幕（PGS/VobSub）必须这么走
- `fontsdir=`：**ASS 的字体/排版依赖它**，不给会退化成默认字体
- `alpha=1`：源带 alpha 时需要
- 网络路径要转义：分号、冒号在 Windows 路径里会被 filtergraph 吃掉（我们 Linux 可以少操心）

### 5.2 外部字幕的字符集（我们库里必然踩，还没处理）

`EncodingHelper.cs:1980` 附近有 `GetSubtitleFileCharacterSet`：按魔数/语言猜
编码，然后给字幕滤镜加 `:charenc=`。**外部 `.srt` 有大量 GB18030/GBK 的
中文文件**（我们库里 95% 是中文内容），现在直接 `-f webvtt` 转出来会变乱码。

M4 要补：抽字幕前先探测编码（BOM → UTF-8；否则试 UTF-8 严格解码，失败按
GB18030），需要时给 ffmpeg 加 `-sub_charenc GB18030`。

### 5.3 文本字幕的形态

`MediaBrowser.MediaEncoding/Subtitles/SubtitleEncoder.cs` + `SubtitleFormatExtensions.cs`

- 一切文本字幕最终都转 **WebVTT** 交给浏览器（和我们 M3 一致）
- ASS/SSA 的样式（字体、位置、描边）在 VTT 里基本丢失；要保真只能烧录
- `SubtitleEditParser.cs`/`ISubtitleParser.cs` 是给「内嵌文本字幕在界面里编辑」
  用的（M6 之后再考虑）

---

## 六、探测（M0 已实现，差异点留档）

- Jellyfin 侧在 `MediaBrowser.Providers/MediaInfo/{FFProbeProvider,ProbeProvider,MediaInfoResolver}.cs`
- 关键点与我们一致：`ffprobe -v error -print_format json -show_format -show_streams`
  （外加 `-analyzeduration`/`-probesize` 上限，避免大文件卡住）
- 我们要补的细节（M4 顺手做）：
  - `side_data_list` 里的 **rotation**（竖屏视频）
  - `color_transfer/primaries/space` → 判断 **HDR**（`VideoRangeTypeNotSupported`
    是 Jellyfin 单独的转码理由）
  - `probe_score` 用来判断「探测结果可不可信」（我们现在只看 `probe_state`）

---

## 七、转码理由的词汇表（照抄语义，不抄代码）

`MediaBrowser.Model/Session/TranscodeReason.cs` —— 用来对齐我们 `Plan.Reasons` 的措辞：

```
ContainerNotSupported           容器不支持
VideoCodecNotSupported          视频编码不支持
VideoProfileNotSupported        档次（Profile）不支持
VideoCodecTagNotSupported       封装标签不支持（例如 hevc 没打 hvc1）
VideoLevelNotSupported          级别不支持
VideoResolutionNotSupported     分辨率超上限
VideoBitDepthNotSupported       色深超上限（10bit H.264 必转）
VideoFramerateNotSupported      帧率超上限
VideoRangeTypeNotSupported      色域/HDR 不支持
RefFramesNotSupported           参考帧数超限
AnamorphicVideoNotSupported     变形宽银幕
InterlacedVideoNotSupported     隔行
AudioCodecNotSupported          音频编码不支持
AudioChannelsNotSupported       声道数超限
AudioProfileNotSupported        …
AudioSampleRateNotSupported     …
AudioBitDepthNotSupported       …
ContainerBitrateExceedsLimit    容器码率超限
VideoBitrateNotSupported        视频码率超限（限速场景）
AudioBitrateNotSupported        音频码率超限
UnknownVideoStreamInfo          探测不到视频流信息
UnknownAudioStreamInfo          探测不到音频流信息
SecondaryAudioNotSupported      副音轨不支持
SubtitleCodecNotSupported       字幕格式不支持
DirectPlayError                 直出直接失败了（兜底）
```

LMBY 的决策引擎已经给中文理由链，M4 扩到「转码」时把上面的语义补全
（尤其是 **VideoBitDepthNotSupported / VideoRangeTypeNotSupported /
InterlacedVideoNotSupported** 这三条我们现在还没判）。

---

## 八、M4 直接照做的清单

1. `internal/stream`：补 `-hls_list_size 0`、`-map_metadata -1 -map_chapters -1`、
   `-max_delay 5000000`；fMP4 加 `movflags=+frag_discont+skip_sidx`
2. 转码参数：`-force_key_frames expr:gte(t,n_forced*N)`（VAAPI）+ libx264 的 `-sc_threshold 0`
3. VAAPI：`-vaapi_device` + `-rc_mode CQP -qp N`（本机约束）+ 滤镜链 `format=nv12|vaapi,hwupload`
4. 节流：**stdin 按键**（`p`/`u`）+ 「已生成 vs 客户端已取」判定；去掉 `-nostdin`
5. 字幕：抽成外挂文件 → 烧录时 `subtitles=f=..:sub2video=1:fontsdir=..`；
   外部字幕加字符集探测（GB18030）
6. `-copyts -avoid_negative_ts disabled`，同时把客户端的窗口偏移簿记删掉
7. 探测补 rotation / HDR / probe_score
8. 理由链词汇对齐 TranscodeReason

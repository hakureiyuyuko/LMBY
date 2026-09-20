# 转码与硬件加速

本文件记录**实测**结果，而不是「理论上支持什么」。
LMBY 的一条硬性原则：**能力必须在运行时实测，不能只看 `ffmpeg -encoders` 的列表。**

## 为什么

`ffmpeg -encoders` 会列出 `h264_qsv`、`av1_qsv` 等一堆编码器，但它们能否工作取决于：

- 本机有没有对应的**运行时实现库**（`libvpl` 只是分发器，真正干活的是 `libmfx-gen` 或 legacy MediaSDK）
- GPU 的**代际**是否被该实现支持
- 容器/权限是否允许访问设备节点

在下面这台机器上，列表里明明有 `h264_qsv`，实际跑起来直接失败 —— 这就是必须实测的原因。

## 测试环境

| 项 | 值 |
|---|---|
| CPU / GPU | Intel Core i5-10500T + **Intel UHD Graphics 630**（Comet Lake，Gen9.5） |
| 系统 | Debian 13 (trixie)，LXC 容器（privileged），`/dev/dri` 直通 |
| ffmpeg | 7.1.5-0+deb13u1 |
| 驱动 | `intel-media-va-driver-non-free` 25.2.3（iHD）、`libvpl2` 2.14、`libmfx-gen1.2` 25.1.4 |

## 结论汇总

| 后端 | 结果 | 证据 |
|---|---|---|
| **VAAPI** | ✅ **可用** | `vainfo` 报告 iHD driver 25.2.3 加载成功，H264/HEVC 同时具备 `VAEntrypointVLD`（解码）与 `VAEntrypointEncSlice`（编码） |
| **QSV** | ❌ **不可用** | `vpl-inspect` 明确报 `Warning - no implementations found by MFXEnumImplementations()`；ffmpeg 报 `Error creating a MFX session: -9` |
| 软件（libx264） | ✅ 可用 | 作为兜底 |

### QSV 为什么不可用

Gen9.5（Comet Lake）需要 **legacy Intel Media SDK**；而 Debian 13 已移除 `intel-mediasdk`，
`libmfx-gen` 只支持 Gen12+（Xe）。两者都不满足，所以 oneVPL 找不到任何实现。

试过并确认无效：安装 `libmfx-gen1.2` 后仍然 `-9`。

> 对本项目的意义：能力探测必须包含「**真跑一小段转码**」这一步，并把结果缓存 +
> 允许用户手动覆盖。只解析 `-encoders` 会得出完全错误的结论。

## 实测性能

素材：`testsrc2` 合成源 1920x1080@25fps，H.264，3 秒（75 帧）。
`real` 时间越短越好；表中为容器内单次运行结果。

| 场景 | 命令要点 | real | 约合 |
|---|---|---|---|
| VAAPI 硬件编码 H264 | `-vaapi_device /dev/dri/renderD128 -vf format=nv12,hwupload -c:v h264_vaapi -rc_mode CQP -qp 24` | 0.35 s | ~8.5x 实时 |
| VAAPI 硬件解码 + 缩放 | `-hwaccel vaapi -hwaccel_output_format vaapi -vf scale_vaapi=w=1280:h=720 -c:v h264_vaapi` | 0.26 s | ~11.5x 实时 |
| VAAPI 硬件编码 HEVC | `-c:v hevc_vaapi -rc_mode CQP -qp 26` | 0.70 s | ~4.3x 实时 |
| 软件 libx264 veryfast | `-c:v libx264 -preset veryfast` | 0.85 s | ~3.5x 实时 |

> 说明：合成画面（testsrc2）编码很快，这些数字**只能横向比较后端**，
> 不能当真实片源的性能预期。真实片源基准属于 M4 的任务。

## 已踩到的参数坑

1. **`-rc_mode` 必须显式给**。本机 VAAPI 驱动只支持 `CQP`：
   用 `-low_power 1` 会报
   `Driver does not support any RC mode compatible with selected options (supported modes: CQP)`。
   → 因此命令模板里用 `-rc_mode CQP -qp N`，而不是常见的 `-b:v`。
2. **`-init_hw_device qsv=...` 在容器里可能产生误导性报错**。
   同一台机器 VAAPI 正常，不代表 QSV 能用 —— 两者的依赖链完全不同。
3. `intel_gpu_top` 可以在容器里正常工作（i915 sysfs 可读），
   M4 做负载监控时可以直接用它。

## 待 M4 补全

- [ ] 把上述命令模板固化成 `internal/transcode` 里的后端定义
- [ ] 能力探测：真跑 1 秒小样 + 结果缓存进 `settings`
- [ ] 用真实片源（1080p H.264 蓝光 rip、4K HEVC、HDR10）做基准
- [ ] HDR → SDR tone mapping（本机 Gen9.5 性能有限，需明确支持矩阵）
- [ ] 节流（预生成 N 片后 SIGSTOP）与并发上限的实测

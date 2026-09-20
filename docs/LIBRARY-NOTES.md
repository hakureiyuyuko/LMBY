# 媒体库命名约定（实测记录）

本文件记录**在真实媒体库上观察到的**目录与命名约定。解析器（`internal/parser`）
与扫描器（`internal/scanner`）的实现就是按这些规则写的，测试语料
`internal/parser/testdata/cases.json` 里的用例也都取自这里。

样本库：`\\<MEDIA_SERVER>\<MEDIA_SHARE>`（约 3 万文件 / 7.5T，由 Emby 刮削过）。

## 1. 目录层级

```
库根/「分类」/作品名 (年份)/
    tvshow.nfo            ← xbmc 格式，含 plot/originaltitle/actor(tmdb+imdb+tvdb id)
    poster.jpg  fanart.jpg  banner.jpg  clearlogo.png  thumb.jpg
    season01-poster.jpg   season04-poster.jpg  season-specials-poster.jpg
    Backdrops/            ← 只放背景图，扫描时整棵跳过
    Season 1/  Season 2/ ...  Season 8/
        S01E01.mkv        ← 正片；本库用纯 SxxExx 编号，不含集标题
        S01E01.nfo        ← 单集元数据
        S01E01-thumb.jpg  ← 单集缩略图
    Specials/
        S00E01.mkv
```

要点：

- **库根可能直接就是作品目录**（例如把一个剧集目录直接加为库根）。解析器必须把
  库根也算进层级链，否则剧集名会退化成 `Season 1`。这是实际踩过的 bug，
  回归测试见 `internal/scanner/scanner_test.go`。
- 分类目录用 `「」` 或 `『』` 包裹：`「完结动画」`、`「高清电影」`、`『华语』`。
  剧集分为 `连载动画 / 完结动画 / 剧场动画 / 连载剧集 / 完结剧集 / 高清电影`。
- 作品目录统一是 `名称 (年份)`，年份用**半角**括号；也见过全角 `（2021）`，两种都要支持。
- 季目录：`Season N`（N 可带前导零）与 `Specials`；中文也可能写成 `第N季`、`特典`。

## 2. 文件命名的两套规范

### 2.1 Emby 风格（本库 82% 的视频）

```
S01E01.mkv          S01E02E03.mkv（连播）      S07E05.mp4
```

- 文件名里**没有标题**，标题来自同目录的 `S01E01.nfo`；nfo 缺失时回退到剧集名。
- 集号可能很大（`S01E129.mkv`），所以集号要支持 1~4 位。
- 也有 `02 - 标题.mkv`、`第3集.mkv`、`1x05`、`Season 1 Episode 5` 等写法。

### 2.2 库主自定的规范（`「基准测试」` 目录，见 `命名参考.txt`）

命名规范原文（UTF-16LE 编码的 txt）：

> ①编码格式：RV40、AVC、VP9、HEVC、AV1、VCC
> ②分辨率：720P、1080P、2K、4K、8K
> ③类型特征：3D、HDR、杜比视界、杜比全景声
> ④色彩范围：YUV420P8、YUV420P10、YUV420P12、YUV422P10、YUV444P10、YUV444P12
> ⑤帧数：24～120fps
> ⑥码率：Mbps
> ⑦名称：《xxx》

实际样本：

```
AVC 1080P YUV420P8 30fps 45Mbps《东芝 - 炫Dazzle》.mkv
AVC 4K YUV420P8 120fps 15Mbps《LoveLive!Superstar!! - 第3话插入歌》.mp4
RV40 720P YUV420P8 25fps 1.5Mbps《满汉全席》.rmvb
HEVC 4K HDR YUV420P10 60fps 50Mbps《杜比视界测试》.mkv
```

解析要点：

- **《》里就是标题**，且要原样保留 —— 里面出现过 `杜比视界`、`HDR`、`第3话`
  这些看起来像规格/集号的词，都不能当规格剥掉或当集号解析。
- 《》**之外**的部分才是技术标记，`internal/parser` 会把 codec/分辨率/色深/
  帧率/码率/特性都提取出来，存进 `media_items.file_tech`。
- 序号数字要防误判：`120fps 15Mbps` 里的 `s 15` 曾被误认成 `S15`（已修，见
  `seasonFromPrefix` 的注释）。

## 3. 附属文件

| 文件 | 归属 | 说明 |
|---|---|---|
| `tvshow.nfo` / `movie.nfo` | 作品 | 目录级元数据 |
| `S01E01.nfo` | 单集 | 与视频同名 |
| `poster.jpg` `folder.jpg` `cover.jpg` | 目录级 | → `poster` |
| `fanart.jpg` `backdrop.jpg` `background.jpg` | 目录级 | → `fanart` |
| `banner.jpg` | 目录级 | → `banner` |
| `clearlogo.png` `logo.png` | 目录级 | → `logo` |
| `season01-poster.jpg` `season-specials-poster.jpg` | 对应季 | 季海报 |
| `<视频名>-poster.jpg` | 同目录同名视频 | 后缀决定类别 |
| `<视频名>-thumb.jpg` `S01E01-thumb.jpg` | 同名的集 | → `thumb` |
| `<视频名>.jpg`（无后缀） | 同名视频 | 按缩略图处理 |
| `<视频名>.ass` `.srt` `.sup` | 同名视频 | 外挂字幕（M1 只统计，不建条目） |
| `Backdrops/` 里的图 | 跳过 | 整棵目录跳过，避免和图库重复 |

## 4. 必须忽略的干扰项

真实库里存在这些文件，扫描时应当静默跳过、不产生「未识别」噪音：

```
autorun.inf                    External USB 3.0.ico           .DS_Store
Thumbs.db                      *.tmp  *.part  *.!qB           *.torrent
```

## 5. 疑难样本（已进测试语料）

```
大欺诈师／GREAT PRETENDER (2020)          ← 作品名含全角斜杠
银河机攻队：庄严王子 (2013)                 ← 全角冒号
86-不存在的战区- (2021)                     ← 结尾连字符属于名字
ENDRO～！ (2019)                            ← 全角波浪号 + 感叹号
LoveLive! 虹之咲学园偶像同好会「四格漫」 (2023)  ← 名字里有「」
内封PGS字幕测试《黑侠 (1996)》16：9.mkv      ← 《》里有年份与全角冒号
122509_740 122509- 740model Collection选择…81圣诞节.avi  ← 纯杂乱名，只要求不崩
电视／盒子CPU性能测试片.mkv                  ← 全角斜杠
```

## 6. 环境注意

这个库通过 CIFS 挂载，**读取单个已存在的小文件（nfo/图片）可能极慢**。
实测 `rsize=4194304`（4MB）时读 5KB 的 nfo 要 **2.1 秒**；
改成 `rsize=131072,wsize=131072,cache=loose,actimeo=60` 后降到 ~11ms。
完整挂载参数与排查过程见 `docs/DEV-ENV.md`。

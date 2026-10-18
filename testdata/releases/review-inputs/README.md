# 独立待审输入池（不计黄金样本）

这两份数据只保存原测试中的字符串字面量、原始标量断言和源文件行号；没有执行外部解析器，也没有使用 PulseMedia 输出生成期望值。

| 来源 | 固定提交 | 原文件 | 去重输入 | 原文件 SHA-256 |
|---|---|---|---:|---|
| `IGI-111/torrent-name-parser` | `6f4f42c2c711c53ed787f535d5b8216e6f47c9c5` | `src/test.rs` | 92 | `2b600d6f0a051cd651b9ff74a3b9d2e528b62ff755b59e0f53c06294eab47d1d` |
| `bhmahler/TorrentTitleParser` | `604b716c30c3338fbc1d73157f8000056a3b5132` | `TorrentTitleParser.Tests/TitleTests.cs` | 27 | `ba1eb5502901f0f369995fc9db6df5827e830042fbab9ce616cad348789b65f3` |

两源均保留各自完整 MIT 许可和 copyright；许可、固定源哈希、导出 JSON 哈希、唯一输入及原断言均受测试保护。与现有四源 683 个输入合并去重后为 **772 个原始输入，新增 89 个**。不是 772 个完整黄金样本；当前完整标注仍是 **40/1000**。

## 权限边界

- `review_status=unreviewed`、`formal_golden_acceptance=false`、`coverage_authority=false` 必须存在。缺失或提升任一声明，测试拒绝。
- `original_fields` 保留上游大小写与值，不归一为 PulseMedia 品质或 canonical 覆盖。例如 Rust `episode()` 可能只给 `e09e10` 的首集 9；这不能证明只有一集，也不能生成区间或单元 UUID。
- 来源没有给出置信、理由、完整单元集合、测量事实或发行组身份授权。不存在的字段不能由解析器填充。
- 重复字符串的断言按不同 `observations` 保留，不静默合并或挑选期待值；动态表达式和无法静态定位的断言不执行、不猜。
- 此池不接入生产解析、获取、映射或整理。只有逐条审核完整合同并接入来源核验后，才能转入 `reviewed-full.json`；仍须满足 M3-02 的完整独立验收。

## 可复现静态提取

维护工具仅用 `.mise.toml` 已锁定的 Node，不增加 Python、Rust、C# 运行时或解析器依赖。先下载固定源和原 MIT 文本到独立 `/data/temp` 目录；提取器校验固定字节哈希，输出仅到新的待审目录，不覆盖既有文件。

```sh
owned=$(mktemp -d /data/temp/pulse-review-inputs.XXXXXX)
trap 'case "$owned" in /data/temp/pulse-review-inputs.*) rm -rf -- "$owned";; esac' EXIT
export TMPDIR="$owned" TMP="$owned" TEMP="$owned"
mkdir -p "$owned/raw"
curl --fail --proto '=https' 'https://raw.githubusercontent.com/IGI-111/torrent-name-parser/6f4f42c2c711c53ed787f535d5b8216e6f47c9c5/src/test.rs' -o "$owned/raw/rust-ptn.source"
curl --fail --proto '=https' 'https://raw.githubusercontent.com/IGI-111/torrent-name-parser/6f4f42c2c711c53ed787f535d5b8216e6f47c9c5/LICENSE' -o "$owned/raw/rust-ptn.LICENSE"
curl --fail --proto '=https' 'https://raw.githubusercontent.com/bhmahler/TorrentTitleParser/604b716c30c3338fbc1d73157f8000056a3b5132/TorrentTitleParser.Tests/TitleTests.cs' -o "$owned/raw/titleparser.source"
curl --fail --proto '=https' 'https://raw.githubusercontent.com/bhmahler/TorrentTitleParser/604b716c30c3338fbc1d73157f8000056a3b5132/LICENSE' -o "$owned/raw/titleparser.LICENSE"
mise exec -- node scripts/extract_release_review_inputs.cjs --raw-dir "$owned/raw" --output-dir "$owned/out"
mise exec -- node --test scripts/extract_release_review_inputs.test.cjs
```

外部测试源只用于静态读取标注，不提交解析器实现。失败产生的部分待审输出应整目录丢弃，不直接发布。其文件名与权限声明也有意不同于完整黄金合同。

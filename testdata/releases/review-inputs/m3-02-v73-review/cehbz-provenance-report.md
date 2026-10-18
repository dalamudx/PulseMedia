# cehbz 固定来源与冻结合同复核

结论：按 M3-02 的实际要求，cehbz 可以作为独立原始字面标题断言来源。固定仓库直接编写/维护的测试表、直接相等比较及 Git 历史提供了独立证据；不需要另行证明每个名称曾在外部 torrent 站出现。不能将这项结论等同于全部样本通过、下载存在证明或整源取得覆盖权限。

- 固定来源：https://github.com/cehbz/torrentname/tree/803fda3c3c111ae99bfb8ecd98eaca0719503068 ，MIT，Copyright (c) 2024 cehbz。
- `parse_test.go` SHA-256：`3848e40b8117f4c447a9f6a350052b5d255c80604d72b73bac04f483878c51df`；Git blob：`cf2b952186def1564e971c81eb9a867f5a70c6f8`。
- `LICENSE` SHA-256：`31aa89620a821a00659bfb779f63b1df54a82e01036569882cd6bbedbf1d0af6`；Git blob：`80c88d065b85fb68d7bc4c619285e8ef88369732`。远端固定提交与仓库保留文件完全一致。

`TestParse` 使用显式 input 字符串及 `expected: &TorrentInfo{Title: ...}`；679 行将实际结果与期望对象传给比较器，693 行开始直接比较 Title。期望值没有调用 Parse；上游 Parse 只生成被比较的实际结果。README 说明普通测试用法，固定树只有 .gitignore、LICENSE、README.md、example/main.go、go.mod、parse.go、parse_test.go，没有生成器、快照目录或生成脚本。测试原始标签与 PulseMedia 的输出无依赖。

五个修改测试文件的历史提交已保存并逐项对照：`f34593c0283b11bff69bd260966e817d276bcdd4`、`169bcb1b624dee58ee36498097f21b7ea52f8383`、`7e04c5187af335e23a8710f734eb19e009f94019`、`31308368aec81260096d73a6e2dbd6bc6be4fe9c`、`545c903b9ec6d2ab014f4b1e1b0803e31b675ab6`。25 个待审输入的完整 input/Title 对均可在这些原始字面测试表中定位。最后一次测试修改主要将 Confidence 字面浮点数改成显式权重表达式，并维护比较器/音轨断言；本次没有执行这些表达式或上游解析器。此证据说明原作者维护的字面断言独立性，不认证作者输入工具或每个下载的实际存在。

原始清点仍为 51 条正向字面断言、47 个去重输入。其中 22 个 synthetic 控制输入继续被原 helper 拒绝，余下 25 个非 synthetic 原标题输入才进入本次复核；重复出现的断言不重复计数。未通过范围展开、样本变体或模板增加数量。

## 当前验证结果

- 解析器固定为 `episode.v67`。
- 25 条冻结合同全部与固定 AST 的原始字段、源位置、置信表达式原文逐一核对，再调用原 `validateCehbzOriginalRebuttals`，没有弱化字面准入。
- 完整输出检查含原始输入、标题、年份、类型、完整季集/keys、绝对编号、动漫标志、置信度、复核状态、reason、版本、全部七项品质及品质来源版本。
- **17 条精确通过、8 条未通过**，25 条均不与现有 gold646 的原始输入重复。
- 单独运行 17 条精确合同及两个原始 cehbz 准入/反证测试，包级退出码 0；`test-v67-passing17.jsonl` 保存绿色回执。
- 现有 `TestReviewedFullReleaseAnnotations` 的 646 个子用例及顶层测试通过，证据保存在 `test-all25-and-gold646.jsonl`。该文件同时保留首轮 overlay 品质解码错误的诊断；随后验证器改为与正式黄金测试一致的 plain alias 解码，最终25条回执以 `test-v67-all25.jsonl` 为准。未改动任何期望值。

## 八条实际未通过项

1. `thrones-season-eight`：完整复核合同要求保留 Game of Thrones，v67 返回空标题；零覆盖拒绝本身保留。
2. `blade-runner-2049`：数字标题/2017 被作多年份歧义，标题带入2017、year=0，决策不符原合同。
3. `odyssey-1968`：2001 与1968的歧义导致标题/年份/覆盖/决策不符。
4. `1941-1979`：1941 与1979的歧义导致标题/年份/覆盖/决策不符。
5. `matrix-year-after-metadata`：标题错误包含 1080p BluRay。
6. `odyssey-year-after-metadata`：作品年份1968未得到原合同结果。
7. `daily-show-calendar`：冻结合同 year=0 与原字面 Year=2023 冲突，被原严格准入拒绝；不是来源独立性问题，也不应通过改标签或放松准入消除。
8. `breaking-bad-season-one`：完整复核合同要求保留 Breaking Bad，v67 返回空标题；零覆盖拒绝本身保留。

具体字段差异见 `verification-results.json`。数字年份项体现当前保守拒绝与固定原合同的冲突，不应据此全局放宽年份/覆盖门禁。

## 给 Main 的交接

`passing17-frozen-records.json` 仅从既有冻结记录复制这17条原合同与 source_record，未从解析器输出生成期望值；可供逐条正式纳入时审查。本次正式 gold 晋升为 **0**，实际仍为 **646/1000**。若 Main 后续正式纳入这17条，才是663/1000；本次不作此计数声明。M3-02整体仍未达到1000门槛。可选额外来源搜集未开展。

共享 gold、manifest、文档、准入 helper、识别器及版本均未编辑，保护文件逐项 SHA-256 复验通过，见 `protected-after-check.txt`。本任务仅写入此拥有的子目录。Git历史打包为 `upstream-history.bundle`，并保留五版源文件、README、许可证及差异证据；没有执行任何上游解析器。Go 缓存、Go 临时构建目录和临时 clone 已删除，无后台任务遗留。任务证据按要求保留。

重放命令：`bash /data/temp/pulse-m3-complete.y5vau8/cehbz-provenance/verify.sh`。它保持共享源码只读，用 overlay 验证全部25条并保留失败，再运行精确17条及严格准入测试；退出时删除自己创建的 Go 缓存。报告所有相对产物路径均相对此目录。

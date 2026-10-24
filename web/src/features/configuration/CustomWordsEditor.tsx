import { useEffect, useState } from "react";
import { preferenceScore, validCustomWords, type CustomWord } from "./policyEditorModel";
import "./word-editor.css";

type Props = { words: CustomWord[]; disabled: boolean; onChange: (words: CustomWord[]) => void };
function WordRow({ value, index, disabled, change, remove }: { value: CustomWord; index: number; disabled: boolean; change: (value: CustomWord) => void; remove: () => void }) {
  const [score, setScore] = useState(() => Number.isFinite(value.score) ? String(value.score) : "");
  useEffect(() => { if (Number.isFinite(value.score)) setScore(String(value.score)); }, [value.score]);
  const mode = value.excluded ? "exclude" : value.required ? "require" : "prefer";
  return <fieldset className="custom-word-row" disabled={disabled}><legend>词规则 {index + 1}</legend>
    <label>字面词<input value={value.word} maxLength={256} onChange={e => change({ ...value, word: e.target.value })} /></label>
    <label>用途<select value={mode} onChange={e => { const next = e.target.value; change({ ...value, required: next === "require", excluded: next === "exclude", score: next === "exclude" ? 0 : Number.isFinite(value.score) && value.score !== 0 ? value.score : next === "prefer" ? 10 : 0 }); }}><option value="prefer">偏好排序</option><option value="require">必须包含</option><option value="exclude">必须排除</option></select></label>
    <label>偏好分<input type="text" inputMode="numeric" value={score} disabled={disabled || value.excluded} aria-invalid={!value.excluded && preferenceScore(score) === null} onChange={e => { setScore(e.target.value); change({ ...value, score: preferenceScore(e.target.value) ?? Number.NaN }); }} /></label>
    <button type="button" className="btn-secondary" onClick={remove}>删除词规则 {index + 1}</button>
  </fieldset>;
}
export function CustomWordsEditor({ words, disabled, onChange }: Props) {
  const valid = validCustomWords(words);
  return <section aria-label="进阶词过滤与偏好" className="custom-words-editor"><h4>词过滤与排序偏好</h4>
    <p>字面匹配，不区分大小写；英文词边界匹配，中文按连续文字匹配。排除优先，所有必含词都要满足；偏好分独立于品质分与 cutoff。</p>
    <p>最多64条，每词最多128个字符，偏好分为 -1000 至 1000 的整数。排除词不计分；普通偏好词需非零分。</p>
    {words.length === 0 ? <p>未设置进阶词规则。</p> : words.map((value, index) => <WordRow key={index} value={value} index={index} disabled={disabled} change={next => onChange(words.map((row, i) => i === index ? next : { ...row }))} remove={() => onChange(words.filter((_, i) => i !== index).map(row => ({ ...row })))} />)}
    {!valid && <p role="alert" className="config-error">词规则尚不完整：请检查空词、重复词、分值与用途，再预览或保存。</p>}
    <button type="button" className="btn-secondary" disabled={disabled || words.length >= 64} onClick={() => onChange([...words.map(row => ({ ...row })), { word: "", score: 10, required: false, excluded: false }])}>添加词规则</button>
  </section>;
}

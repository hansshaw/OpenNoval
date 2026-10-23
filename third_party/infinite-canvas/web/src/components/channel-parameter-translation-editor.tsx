"use client";

import { javascriptLanguage } from "@codemirror/lang-javascript";
import CodeMirror, { EditorView } from "@uiw/react-codemirror";
import { App, Button, Collapse, Modal, Space, Typography, theme } from "antd";
import { Copy } from "lucide-react";
import { useRef, useState } from "react";

import { useCopyText } from "@/hooks/use-copy-text";
import { translationAuthoringPrompt, translationConfigItems, translationPrograms, translationResultHint, translationRuleHint, translationTemplate, translationVariableGroups, type TranslationVariable } from "@/lib/channel-parameter-translation";
import { useThemeStore } from "@/stores/use-theme-store";

const editorTheme = EditorView.theme({
    "&": { backgroundColor: "var(--ant-color-bg-container)", color: "var(--ant-color-text)" },
    ".cm-gutters": { backgroundColor: "var(--ant-color-fill-quaternary)", color: "var(--ant-color-text-tertiary)", border: "none" },
    ".cm-activeLine, .cm-activeLineGutter": { backgroundColor: "var(--ant-color-fill-quaternary)" },
    ".cm-cursor": { borderLeftColor: "var(--ant-color-text)" },
    ".cm-selectionBackground, &.cm-focused .cm-selectionBackground": { backgroundColor: "var(--ant-control-item-bg-active)" },
    "&.cm-focused": { outline: "none" },
});
const editorExtensions = [javascriptLanguage, editorTheme];

function StepHeading({ index, title }: { index: number; title: string }) {
    return (
        <div className="mb-2 flex items-center gap-2">
            <span className="inline-flex size-5 shrink-0 items-center justify-center rounded-full bg-stone-900 text-[11px] font-medium text-white dark:bg-stone-100 dark:text-stone-900">{index}</span>
            <span className="text-sm font-medium text-stone-800 dark:text-stone-100">{title}</span>
        </div>
    );
}

export function ChannelParameterTranslationEditor({ name, baseUrl, models, value, onSave, onClose }: { name?: string; baseUrl?: string; models: string[]; value: string; onSave: (source: string) => void; onClose: () => void }) {
    const { message } = App.useApp();
    const copyText = useCopyText();
    const { token } = theme.useToken();
    const dark = useThemeStore((state) => state.theme === "dark");
    const [open, setOpen] = useState(true);
    const [draft, setDraft] = useState(value);
    const [saving, setSaving] = useState(false);
    const view = useRef<EditorView | null>(null);
    const close = () => setOpen(false);
    const insert = (text: string) => {
        if (!view.current) return;
        const { from, to } = view.current.state.selection.main;
        view.current.dispatch({ changes: { from, to, insert: text }, selection: { anchor: from + text.length } });
        view.current.focus();
    };
    const item = (label: string, code: string, description: string, type?: string) => (
        <button key={label} type="button" onClick={() => insert(code)} className="group block w-full rounded-lg border border-transparent px-2.5 py-2 text-left transition-colors hover:border-stone-200 hover:bg-white dark:hover:border-stone-700 dark:hover:bg-stone-800/60">
            <div className="flex flex-wrap items-baseline gap-1.5">
                <code className="whitespace-pre-wrap break-all rounded bg-stone-200/80 px-1.5 py-0.5 font-mono text-[11px] font-semibold text-stone-800 group-hover:bg-blue-100 group-hover:text-blue-700 dark:bg-stone-800 dark:text-stone-100 dark:group-hover:bg-blue-950 dark:group-hover:text-blue-300">{code}</code>
                {type && <span className="font-mono text-[10px] text-stone-400">{type}</span>}
            </div>
            <div className="mt-1 text-xs leading-5 text-stone-500 dark:text-stone-400">{label}：{description}</div>
        </button>
    );
    const variables = (items: readonly TranslationVariable[]) => <div className="space-y-1.5">{items.map(([label, code, type, description]) => item(label, code, description, type))}</div>;
    const save = async () => {
        setSaving(true);
        try {
            await translationPrograms(draft);
            onSave(draft.trim());
            close();
        } catch (error) {
            message.error(error instanceof Error ? error.message : "配置校验失败");
        } finally {
            setSaving(false);
        }
    };
    return (
        <Modal
            open={open}
            title={null}
            footer={null}
            width="100vw"
            onCancel={close}
            afterClose={onClose}
            style={{ top: 0, margin: 0, maxWidth: "100vw", paddingBottom: 0 }}
            styles={{ wrapper: { overflow: "hidden" }, container: { height: "100dvh", padding: 0, borderRadius: 0, overflow: "hidden" }, body: { height: "100dvh" } }}
        >
            <div className="flex h-dvh flex-col overflow-hidden" style={{ color: token.colorText, background: token.colorBgContainer }}>
                <header className="shrink-0 border-b border-[var(--ant-color-border-secondary)] px-6 py-3 pr-14">
                    <div className="text-base font-semibold">{name || "当前渠道"} · 自定传参转译</div>
                    <Typography.Text type="secondary" className="text-xs">
                        空配置使用默认调用；填写后对应模型优先使用。编辑内容随渠道保存生效。
                    </Typography.Text>
                </header>
                <div className="flex min-h-0 flex-1 overflow-hidden">
                    <aside className="flex h-full w-[420px] shrink-0 flex-col border-r border-stone-200 bg-stone-50/80 dark:border-stone-800 dark:bg-stone-900/40">
                        <div className="flex shrink-0 gap-2 border-b border-stone-200/70 px-5 py-3 text-xs text-stone-500 dark:border-stone-800/70 dark:text-stone-400">
                            <span>1. 看懂规则</span><span>→</span><span>2. 让外部 AI 写</span><span>→</span><span>3. 粘贴保存</span>
                        </div>
                        <div className="min-h-0 flex-1 overflow-y-scroll overscroll-contain p-0">
                            <section className="border-b border-stone-200/70 px-5 py-4 dark:border-stone-800/70">
                                <StepHeading index={1} title="看懂规则" />
                                <p className="text-xs leading-5 text-stone-600 dark:text-stone-300">{translationRuleHint}</p>
                                <div className="mt-3">
                                    <div className="mb-1 text-[11px] font-semibold uppercase tracking-wide text-stone-400">返回要求</div>
                                    <div className="text-xs leading-6 text-stone-600 dark:text-stone-300">{translationResultHint}</div>
                                </div>
                            </section>
                            <section className="border-b border-stone-200/70 px-5 py-4 dark:border-stone-800/70">
                                <StepHeading index={2} title="让外部 AI 写" />
                                <ol className="list-decimal space-y-1.5 pl-4 text-xs leading-5 text-stone-600 dark:text-stone-300">
                                    <li>点击下方按钮，复制本页的变量、返回要求和写法说明。</li>
                                    <li>打开 ChatGPT、Claude 或其他 AI，先粘贴说明，再附上你要对接的接口文档。</li>
                                    <li>把 AI 返回的配置粘贴到右侧编辑器，确认后保存。</li>
                                </ol>
                                <p className="mt-2 text-[11px] leading-5 text-stone-400">复制内容包含当前渠道地址、已选模型列表、返回要求、全部可用变量、配置字段和写法约束；密钥使用渠道现有设置。</p>
                                <Button type="primary" className="mt-3" icon={<Copy className="size-3.5" />} onClick={() => copyText(translationAuthoringPrompt(models, baseUrl), "配置说明已复制，请到你的 AI 里粘贴")}>
                                    复制配置说明
                                </Button>
                            </section>
                            <section className="px-5 py-4">
                                <StepHeading index={3} title="可用变量" />
                                <div className="mb-2 flex items-center justify-between">
                                    <p className="text-xs leading-5 text-stone-500 dark:text-stone-400">点击变量名插入到右侧编辑器。</p>
                                    <span className="shrink-0 text-[10px] text-stone-400">点击插入</span>
                                </div>
                                <div className="mb-2 text-base font-semibold">通用变量</div>
                                {variables(translationVariableGroups.common)}
                                <div className="mb-2 mt-4 text-base font-semibold">请求、结果与任务查询</div>
                                <div className="space-y-1.5">{translationConfigItems.map(([label, code, description]) => item(label, code, description))}</div>
                            </section>
                            <Collapse
                                ghost
                                classNames={{ title: "text-base font-semibold" }}
                                className="[&_.ant-collapse-header]:!px-5 [&_.ant-collapse-header]:!py-3 [&_.ant-collapse-content-box]:!px-5"
                                items={(
                                    [
                                        ["image", "图片变量"],
                                        ["video", "视频变量"],
                                        ["audio", "音频变量"],
                                    ] as const
                                ).map(([key, label]) => ({ key, label, children: variables(translationVariableGroups[key]) }))}
                            />
                        </div>
                    </aside>
                    <div className="flex min-w-0 flex-1 flex-col">
                        <div className="flex shrink-0 justify-between border-b border-[var(--ant-color-border-secondary)] px-4 py-3 text-xs">
                            <span>渠道配置</span>
                            <Typography.Text type="secondary">{draft.trim() ? "已填写自定义配置" : "默认调用"}</Typography.Text>
                        </div>
                        <CodeMirror
                            value={draft}
                            onChange={setDraft}
                            onCreateEditor={(editor) => {
                                view.current = editor;
                            }}
                            height="100%"
                            theme={dark ? "dark" : "light"}
                            extensions={editorExtensions}
                            basicSetup={{ autocompletion: false }}
                            placeholder="留空使用系统默认调用，或点击下方插入渠道模板"
                            className="min-h-0 flex-1 [&_.cm-editor]:h-full [&_.cm-scroller]:overflow-auto"
                            style={{ fontSize: 13 }}
                        />
                    </div>
                </div>
                <footer className="flex shrink-0 flex-wrap items-center justify-between gap-3 border-t border-[var(--ant-color-border-secondary)] px-6 py-3">
                    <Space wrap>
                        <Button size="small" onClick={() => insert(translationTemplate(models[0]))}>
                            插入渠道模板
                        </Button>
                        <Button size="small" danger onClick={() => setDraft("")}>
                            恢复默认调用
                        </Button>
                    </Space>
                    <Space>
                        <Button onClick={close}>取消</Button>
                        <Button type="primary" loading={saving} onClick={() => void save()}>
                            保存
                        </Button>
                    </Space>
                </footer>
            </div>
        </Modal>
    );
}

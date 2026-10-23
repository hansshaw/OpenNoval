// 每次调用独立保存配置和函数；主线程为每次同步求值设置一秒上限。
export {};

type Selector = string | ((value: unknown) => unknown) | undefined;
type Rule = Record<string, unknown>;
let root: Rule, rule: Rule, poll: Rule | undefined;
const methods = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"];
const select = (selector: Selector, value: unknown) => {
    if (typeof selector === "function") {
        const result = selector(value);
        if (result && typeof (result as Promise<unknown>).then === "function") throw new Error("响应提取函数必须同步返回结果");
        return result;
    }
    if (selector === undefined || selector === null || selector === "") return value;
    if (typeof selector !== "string") throw new Error("响应提取必须是字段路径或同步函数");
    return selector.split(".").reduce<unknown>((result, field) => (result != null ? (result as Record<string, unknown>)[field] : undefined), value);
};

self.onmessage = (event: MessageEvent) => {
    try {
        const { action, code, names, variables, query, data } = event.data;
        let result: unknown;
        if (action === "init") {
            root = new Function(...names, `"use strict"; return (${code});`)(...names.map((name: string) => variables[name]));
            rule = root.rule as Rule;
            const configuredPoll = rule.poll === undefined ? root.poll : rule.poll;
            if (configuredPoll !== undefined && configuredPoll !== false && (!configuredPoll || typeof configuredPoll !== "object" || Array.isArray(configuredPoll))) throw new Error("poll 必须是查询配置对象或 false");
            poll = configuredPoll ? (configuredPoll as Rule) : undefined;
            if (poll && (poll.taskId === undefined || poll.status === undefined || !Array.isArray(poll.success) || !poll.success.length)) throw new Error("任务查询需要 taskId、status 和非空 success 状态值");
            if (poll?.failure !== undefined && !Array.isArray(poll.failure)) throw new Error("failure 必须是状态值数组");
            result = Boolean(poll);
        } else if (action === "request") {
            const current = query ? poll : rule;
            const configured = methods.filter((method) => current?.[method] !== undefined);
            if (configured.length !== 1) throw new Error("必须填写且仅填写一种请求方法");
            result = { method: configured[0], request: current![configured[0]], headers: root.headers || {} };
        } else if (action === "error") {
            result = rule.error ? select(rule.error as Selector, data) : (data as { error?: { message?: string }; msg?: string })?.error?.message || (data as { msg?: string })?.msg || (typeof data === "string" ? data : undefined);
        } else {
            const failure = rule.error ? select(rule.error as Selector, data) : (data as { error?: { message?: string } })?.error?.message;
            if (failure) throw new Error(typeof failure === "string" ? failure : JSON.stringify(failure));
            if (query) {
                const status = select(poll!.status as Selector, data);
                if ((poll!.failure as unknown[] | undefined)?.includes(status)) throw new Error(`上游任务失败：${String(status)}`);
                const complete = (poll!.success as unknown[]).includes(status);
                result = {
                    status: complete ? "completed" : "processing",
                    progress: complete ? 100 : Math.max(0, Math.min(100, Number(poll!.progress === undefined ? 0 : select(poll!.progress as Selector, data)) || 0)),
                    value: complete ? select(rule.result as Selector, data) : undefined,
                    defaultResult: rule.result == null || rule.result === "",
                };
            } else if (poll) {
                const ids = select(poll.taskId as Selector, data);
                const values = (Array.isArray(ids) ? ids : [ids]).filter((id) => id != null && id !== "");
                if (values.some((id) => typeof id !== "string" && typeof id !== "number")) throw new Error("任务 ID 必须是字符串、数字或它们的数组");
                const taskIds = [...new Set(values.map(String))];
                if (!taskIds.length) throw new Error("提交响应没有返回配置位置的任务 ID");
                result = { status: "processing", progress: 0, taskIds };
            } else result = { status: "completed", progress: 100, value: select(rule.result as Selector, data) };
        }
        self.postMessage({ result: result === undefined ? undefined : JSON.parse(JSON.stringify(result)) });
    } catch (error) {
        self.postMessage({ error: error instanceof Error ? error.message : "自定配置求值失败" });
    }
};

// 用户配置只生成描述；网络与计时由外部执行器处理。
for (const name of ["fetch", "XMLHttpRequest", "WebSocket", "importScripts", "setTimeout", "setInterval"]) Object.defineProperty(self, name, { value: undefined });

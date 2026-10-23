import localforage from "localforage";
import { create } from "zustand";

import type { ChannelTranslation } from "@/lib/channel-parameter-translation";

const storage = localforage.createInstance({ name: "infinite-canvas", storeName: "channel_translations" });
let writeQueue = Promise.resolve();
type TranslationStore = {
    accounts: Record<string, Record<string, string>>;
    revisions: Record<string, number>;
    load: (accountId: string) => Promise<Record<string, string>>;
    list: (accountId: string, channelIds: string[]) => Promise<ChannelTranslation[]>;
    save: (accountId: string, channelId: string, source: string) => Promise<void>;
    replace: (accountId: string, records: ChannelTranslation[], expectedRevision?: number) => Promise<void>;
};

export const useChannelTranslationStore = create<TranslationStore>((set, get) => ({
    accounts: {},
    revisions: {},
    load: async (accountId) => {
        await writeQueue;
        const cached = get().accounts[accountId];
        if (cached) return cached;
        const records = (await storage.getItem<Record<string, string>>(encodeURIComponent(accountId))) || {};
        if (get().accounts[accountId]) return get().accounts[accountId];
        set((state) => ({ accounts: { ...state.accounts, [accountId]: records } }));
        return records;
    },
    list: async (accountId, channelIds) => {
        const records = await get().load(accountId);
        return channelIds.filter((id) => records[id]).map((channelId) => ({ channelId, parameterTranslation: records[channelId] }));
    },
    save: async (accountId, channelId, source) => {
        if (!channelId) throw new Error("缺少渠道 ID");
        const next = writeQueue.then(async () => {
            const records = { ...(get().accounts[accountId] || (await storage.getItem<Record<string, string>>(encodeURIComponent(accountId))) || {}) };
            if (source.trim()) records[channelId] = source.trim();
            else delete records[channelId];
            await storage.setItem(encodeURIComponent(accountId), records);
            set((state) => ({ accounts: { ...state.accounts, [accountId]: records }, revisions: { ...state.revisions, [accountId]: (state.revisions[accountId] || 0) + 1 } }));
        });
        writeQueue = next.catch(() => undefined);
        return next;
    },
    replace: async (accountId, records, expectedRevision) => {
        const next = writeQueue.then(async () => {
            if (expectedRevision !== undefined && expectedRevision !== (get().revisions[accountId] || 0)) return;
            const values = Object.fromEntries(records.filter((item) => item.parameterTranslation.trim()).map((item) => [item.channelId, item.parameterTranslation]));
            // 云端原文仍可在内存使用，本地缓存失败不阻断其他账号配置加载。
            await storage.setItem(encodeURIComponent(accountId), values).catch(() => undefined);
            set((state) => ({ accounts: { ...state.accounts, [accountId]: values } }));
        });
        writeQueue = next.catch(() => undefined);
        return next;
    },
}));

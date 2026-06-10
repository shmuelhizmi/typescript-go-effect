declare module "monaco-editor/esm/vs/basic-languages/typescript/typescript.js" {
    import type { languages } from "monaco-editor";
    export const conf: languages.LanguageConfiguration;
    export const language: languages.IMonarchLanguage & {
        keywords: string[];
        operators: string[];
    };
}

declare module "monaco-editor/esm/vs/editor/editor.worker?worker" {
    const WorkerFactory: new () => Worker;
    export default WorkerFactory;
}

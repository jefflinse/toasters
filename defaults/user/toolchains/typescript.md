---
id: typescript
name: TypeScript
description: The TypeScript/JavaScript toolchain.
vars:
  version:
    description: The target TypeScript version.
    default: "5.8"
---

The current version of TypeScript is {{ vars.version }}.
TypeScript has mature type inference — avoid unnecessary type annotations.
Use `const` by default, `let` only when reassignment is needed.
Use ES modules (import/export), not CommonJS (require).
Prefer `interface` over `type` for object shapes unless unions or intersections are needed.
Use `strict: true` in tsconfig.json.
Use `unknown` over `any` wherever possible.
Use optional chaining (`?.`) and nullish coalescing (`??`).
Prefer `Array.prototype` methods (map, filter, reduce) over imperative loops.
Use `async`/`await` over raw Promises.

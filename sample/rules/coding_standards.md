# JavaScript & Google Apps Script Coding Standards

## 1. Variable Declarations & Immutability
- **Forbid `var`**: Never use `var` due to function-scoping and hoisting hazards.
- **Default to `const`**: Declare all variables with `const` by default unless the binding must be reassigned.
- **Use `let` Sparingly**: Use `let` only for loop counters or state variables that explicitly require reassignment.

## 2. Asynchronous Operations & Error Handling
- **Use `async` / `await`**: Always `await` Promises (`fetch()`, `response.json()`) inside `async` functions rather than treating asynchronous return values as synchronous data.
- **Explicit HTTP Status Validation**: Check `if (!response.ok)` (or inspect `getResponseCode()` in `UrlFetchApp`) before parsing the response body, and throw a descriptive `Error` on non-2xx status codes.
- **Structured `try...catch`**: Wrap network calls and JSON parsing in `try...catch` blocks with actionable error logging or propagation.

## 3. Security, URLs & Maintainability
- **Enforce HTTPS**: Always use `https://` endpoints; never transmit requests over plaintext `http://`.
- **Input Validation & Encoding**: Validate parameters (`id`, query strings) and encode dynamic path/query segments with `encodeURIComponent()`.
- **No Hardcoded Secrets**: Retrieve API keys and credentials from environment variables or `PropertiesService` rather than hardcoding them in source files.

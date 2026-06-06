Security. Look for:

- Injection (SQL, shell/command, template) from unsanitized input.
- Path traversal and unsafe construction of file paths from input.
- Hardcoded secrets, credentials, API keys, or tokens.
- Unsafe operations: unchecked deserialization, unsafe reflection, eval-like calls.
- Missing input validation or authorization checks on entry points.
- Sensitive data in logs or error messages.

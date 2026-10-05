---
"posthog-go": patch
---

Keep empty typed slices (`[]string{}`, `[]bool{}`, `[]int{}`, `[]int64{}`, `[]float64{}`) as `[]` when a `BeforeSend` hook is set. The hook's copy of the message used to send them as `null`.

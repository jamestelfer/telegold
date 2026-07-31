package telegold

import "github.com/jamestelfer/telegold/tgtest"

// assertTagBalanced is the R5 invariant check, applied to every block this
// package's tests render and every chunk they pack.
//
// It lives in tgtest because the adapter's tests assert the same property
// on the bytes that reach Telegram, and two copies would eventually disagree
// about what balanced means — the tag allowlist inside it is a live policy
// list, not a constant. Aliased rather than qualified because the name has
// call sites throughout this package's tests.
var assertTagBalanced = tgtest.AssertTagBalanced

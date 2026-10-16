package generate

import (
	"bytes"
	"fmt"
	"os"

	"github.com/edwinhuish/twin/internal/sysx"
)

// syncBwrapCopy keeps the private bubblewrap copy byte-identical to the system
// one.
//
// A copy, not a link, and that is the whole point: AppArmor matches on the
// binary's *path*, so /usr/bin/bwrap carries the profile that stacks
// unpriv_bwrap onto every descendant while /home/.../twin/bin/bwrap carries
// none. A symlink would resolve back to the profiled path and buy nothing.
//
// Refreshing on every wrap and reinstall is deliberate. A stale copy is a silent
// problem — it keeps working right up until the day a bubblewrap fix matters —
// so the comparison happens where a human is already watching.
func syncBwrapCopy(dst string) error {
	src := sysx.FirstInstalled("bwrap")
	if src == "" {
		return fmt.Errorf("找不到系统 bwrap，建不出私有副本（先 sudo apt install bubblewrap）")
	}
	if same, err := filesEqual(src, dst); err == nil && same {
		return nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("读不到 %s：%w", src, err)
	}
	if err := sysx.WriteFileAtomic(dst, data, 0o755); err != nil {
		return fmt.Errorf("写不出 bwrap 副本 %s：%w", dst, err)
	}
	return nil
}

func filesEqual(a, b string) (bool, error) {
	da, err := os.ReadFile(a)
	if err != nil {
		return false, err
	}
	db, err := os.ReadFile(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(da, db), nil
}

// writeBootstrap writes the script the trampoline runs.
//
// It is regenerated on every wrap and reinstall rather than versioned: the file is
// tiny, its content is a constant, and a stale one would be invisible.
func writeBootstrap(dst string) error {
	return sysx.WriteFileAtomic(dst, []byte(bootstrapTemplate), 0o644)
}

// bootstrapTemplate is the Node script that sits between the trampoline and the
// bubblewrap copy.
//
// Kept as a literal rather than shipped as a file so that a wrapped instance
// stays self-contained: everything twin needs at run time is written by
// twin itself.
const bootstrapTemplate = `'use strict';
/*
 * twin 跳板脚本 —— 由 twin 生成，不要手改。
 *
 * 只有一个职责：把「私有 bwrap 副本」的执行，放进跳板程序的 AppArmor 标签里。
 *
 * 为什么需要：本机 kernel.apparmor_restrict_unprivileged_userns=1，只有 profile
 * 里写了 userns 许可的程序才能建非特权 user namespace。bwrap 副本故意不带任何
 * profile —— 好躲开 /usr/bin/bwrap 那份（它会给后代叠 unpriv_bwrap，而
 * unpriv_bwrap 带 audit deny capability，Chromium 沙箱在里面建不起来）。代价是
 * 副本自己也被内核拒。所以先 exec 一个自带 flags=(unconfined){userns,} profile
 * 的程序（通常是某个 Electron 应用），让内核在 exec 时把标签换掉，再由它 spawn
 * 副本 —— 副本及其后代就都带着那个标签了。
 *
 * 用法（由生成的 launcher 调用）：<跳板> bootstrap.js <bwrap> <bwrap 参数…>
 */
const fs = require('fs');
const { spawn } = require('child_process');

// 必须删掉。漏进沙箱的话，被包裹的程序（若它自己也是 Electron）会以 Node
// 模式启动 —— 进程在、窗口不出来。
delete process.env.ELECTRON_RUN_AS_NODE;

const [, , bwrap, ...args] = process.argv;
if (!bwrap) {
  console.error('[twin] 缺少参数：需要 <bwrap 路径> [args...]');
  process.exit(2);
}

// 自检：标签必须真的被换掉。仍是 unconfined 说明跳板程序的 profile 不见了
// （换了机器、应用被卸载、或升级后不再带 profile）。这时 bwrap 必定失败并报
// "setting up uid map: Permission denied" —— 与其让它报，不如提前说清楚，
// 并且给出修法。
try {
  const label = fs.readFileSync('/proc/self/attr/current', 'utf8').trim();
  if (/^unconfined\b/.test(label) || label === '') {
    console.error(
      '[twin] 警告: 当前 AppArmor 标签是 "' + label + '"（没有被换掉）。\n' +
      '          跳板程序的 profile 可能已经不存在了。\n' +
      '          修法: twin reinstall <实例 id>   —— 会重新挑一个跳板。'
    );
  }
} catch (_) {
  /* 没有 AppArmor 的机器上读不到这个文件，忽略 */
}

const child = spawn(bwrap, args, { stdio: 'inherit' });

child.on('error', (err) => {
  console.error('[twin] 无法启动 bwrap: ' + err.message);
  process.exit(127);
});

child.on('exit', (code, signal) => {
  if (signal) {
    // 原样把信号传回自己，保持退出语义
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code === null ? 1 : code);
});
`

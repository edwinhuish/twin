package userns

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/edwinhuish/twin/internal/sysx"
)

// ProfileName is the profile name in the policy twin generates. It is the
// string the kernel's loaded-profile list shows, so it is also how the read-back
// recognises its own work.
const ProfileName = "twin-bwrap"

// ProfileFile is where the fallback profile belongs.
const ProfileFile = "/etc/apparmor.d/twin-bwrap"

// loadedProfiles is the kernel's list of profiles it is actually enforcing.
const loadedProfiles = "/sys/kernel/security/apparmor/profiles"

// ProfileText is the policy that grants one bubblewrap copy the userns
// permission directly, removing the need for a trampoline.
//
// The attachment is the *path*, never the process name: the kernel keys the
// label transition off the path at exec, and the profile name is only a label
// for humans. Attaching to a name would also mean this policy could be shadowed
// by any other program that happens to be called bwrap.
func ProfileText(copyPath string) string {
	return "abi <abi/4.0>,\n" +
		"include <tunables/global>\n" +
		"\n" +
		"profile \"" + ProfileName + "\" \"" + copyPath + "\" flags=(unconfined) {\n" +
		"  userns,\n" +
		"}\n"
}

// ProfileHint is the same policy prefaced with the commands that install it —
// for the runs where twin cannot run them itself (no sudo, or no terminal to
// answer a password prompt on).
func ProfileHint(copyPath string) string {
	return "# 写到 " + ProfileFile + " 后执行：\n" +
		"#   sudo apparmor_parser -r " + ProfileFile + "\n" +
		ProfileText(copyPath)
}

// ProfileCovers reports whether the profile twin installs is on disk and
// attached to path, with the evidence for the verdict.
//
// This is the question `sandbox.Resolve` has to answer before deciding whether
// a bubblewrap copy still needs a trampoline. Being on disk is the right test
// there: AppArmor's init loads every file under /etc/apparmor.d at boot, so a
// profile that is present is a permission that comes back after a reboot. That
// is what makes it different from the `gsettings set` kind of fake success,
// where the command returns 0, nothing lands anywhere, and the setting is gone
// the next time anyone looks.
func ProfileCovers(path string) (bool, string) {
	if path == "" {
		return false, ""
	}
	data, err := os.ReadFile(ProfileFile)
	if err != nil {
		return false, ""
	}
	for _, d := range parseProfiles(string(data)) {
		if !d.grantsUserns() {
			continue
		}
		for _, att := range ExpandBraces(d.attach) {
			if att == path {
				return true, ProfileFile + " 里的 profile " + d.name + " 挂在 " + path + " 上"
			}
		}
	}
	return false, ""
}

// Loaded reports whether the kernel has the profile loaded right now.
//
// couldTell separates "it is not loaded" from "I was not allowed to look":
// the mode bits on the list file say 0444, but the directory above it is what
// actually keeps an unprivileged process out on the machines this was checked
// on (2026-10-12: `head` on it returns EACCES). Reporting a permission problem
// as a missing profile would send the user to reinstall something that is
// already in place.
func Loaded() (loaded, couldTell bool) {
	b, err := os.ReadFile(loadedProfiles)
	if err != nil {
		return false, false
	}
	return loadedIn(string(b)), true
}

// loadedIn reads the kernel's list format: one profile per line, the name
// first, the mode in parentheses after it.
//
// The name has to match whole. A prefix test would let a leftover
// twin-bwrap-something answer for the profile that is actually missing.
func loadedIn(list string) bool {
	for _, line := range strings.Split(list, "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == ProfileName {
			return true
		}
	}
	return false
}

// InstallProfile writes the policy into ProfileFile and loads it, running the
// two commands that need root through sudo.
//
// stagingDir receives the policy text first, which costs no privilege: the only
// two things that ever ask for a password are then the copy and the load — the
// same two the printed fallback would have told the user to run by hand. The
// staging file is removed again, so a failed install leaves nothing behind.
//
// The return value is what could be verified, not what was attempted:
// apparmor_parser exiting 0 means the kernel accepted the policy, and the
// loaded-profile list is read back when this process is allowed to see it.
func InstallProfile(copyPath, stagingDir string) (note string, err error) {
	sudo := sysx.Which("sudo")
	if sudo == "" {
		return "", fmt.Errorf("PATH 上没有 sudo")
	}
	if err := sysx.EnsureDir(stagingDir); err != nil {
		return "", err
	}
	stage := filepath.Join(stagingDir, ProfileName)
	if err := sysx.WriteFileAtomic(stage, []byte(ProfileText(copyPath)), 0o644); err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(stage) }()

	if err := sysx.RunInteractive(sudo, "install", "-m", "0644", "-o", "root", "-g", "root",
		stage, ProfileFile); err != nil {
		return "", err
	}
	if err := sysx.RunInteractive(sudo, "apparmor_parser", "-r", ProfileFile); err != nil {
		return "", err
	}

	loaded, couldTell := Loaded()
	switch {
	case loaded:
		return ProfileFile + " 已写出并用 apparmor_parser 加载，内核的已加载列表里也能读到 " + ProfileName, nil
	case !couldTell:
		return ProfileFile + " 已写出并用 apparmor_parser 加载（这台机器不允许非 root 读内核的已加载列表，" +
			"所以只确认到 apparmor_parser 的退出码）", nil
	default:
		return "", fmt.Errorf("%s 写出去了、apparmor_parser 也返回 0，但内核的已加载列表里没有 %s —— "+
			"这次授权没有真正生效，别把它当成成功", ProfileFile, ProfileName)
	}
}

#!/bin/sh
#
# 把 option-berth 装到的那个目录放进 PATH —— `mage install` 的最后一步。
#
# 为什么要有这一步：`mage install` 把二进制放在 ~/.local/bin，而 macOS 上这个目录
# 默认不在 PATH 里（zsh 不读 ~/.profile，只有 bash 那条路读）。于是 `option-berth` 在
# 终端里敲不到，按 skill 敲它的 agent 也敲不到 —— 而这一步失败是静默的：二进制装是
# 装上了，doctor 里那条 cli_on_path 才是唯一会说的。
#
# 做法照 uv / rustup 的安装脚本（装到用户目录，再往 shell 的启动文件里加一行）：
#
#   - 只改**已经存在**的启动文件 —— 不替人建 .zshrc，也不碰 fish 那种另一套语法
#   - 每个文件各判各的：里面已经提过 .local/bin 就跳过，所以能反复跑
#   - 改了什么就打印什么，一行都没写时不假装做了事
#   - NO_MODIFY_PATH=1 → 一个文件都不动，只把那行打出来（CI 和洁癖用同一条路）
#
# 撤销：删掉启动文件里带 .local/bin 的那一行 —— 打印出来的文件名就是去哪儿删。
set -eu

dir="${1:-$HOME/.local/bin}"

# 写进启动文件的那一行用 $HOME 拼，不用展开后的绝对路径 —— 这行要跟着文件走，
# 而别人的家目录不一定和这里一样。
case "$dir" in
"$HOME"/*) expr='$HOME'"${dir#"$HOME"}" ;;
*) expr="$dir" ;;
esac
line="export PATH=\"$expr:\$PATH\""

# 看一个文件里有没有提过这个目录。用 read 扫而不是 grep：这一条要在任何环境里都算数，
# 而 grep 在某些沙箱里会静默漏匹配 —— 漏了就会重复写。
mentions() {
	while IFS= read -r l || [ -n "$l" ]; do
		case "$l" in
		*".local/bin"*) return 0 ;;
		esac
	done <"$1"
	return 1
}

# 这些是「会读 PATH 的启动文件」里最常见的几个。zsh 两个都写是刻意的：
# .zshenv 每个 zsh 都读（脚本、agent 起的非交互 shell 也算），.zshrc 是交互式那个。
candidates=".zshrc .zshenv .bashrc .bash_profile .bash_login .profile"

if [ "${NO_MODIFY_PATH:-0}" != "0" ]; then
	echo "PATH       没动你的启动文件（NO_MODIFY_PATH=$NO_MODIFY_PATH）。要让 option-berth 在终端里能用，把这一行加到你的启动文件："
	echo
	echo "    $line"
	exit 0
fi

wrote=""
kept=""
for f in $candidates; do
	[ -f "$HOME/$f" ] || continue
	if mentions "$HOME/$f"; then
		kept="$kept $f"
		continue
	fi
	{
		printf '\n# option-berth — the CLI `mage install` puts in %s\n' "$expr"
		printf '%s\n' "$line"
	} >>"$HOME/$f"
	wrote="$wrote $f"
done

if [ -n "$wrote" ]; then
	echo "PATH       往 ${wrote# } 各加了一行 —— 新开的终端里生效（当前这个 shell 要先 . ~/.zshrc）"
else
	echo "PATH       ~/.local/bin 已经在 ${kept# } 里了 —— 没动"
fi

# slopbox default zsh config (seeded into the persistent home on first run).
# Safe to edit — your changes persist in the home mount.

export EDITOR=hx
export PATH="$HOME/.local/bin:$PATH"

# Homebrew (Linuxbrew), if bootstrapped into the persistent home.
if [ -d /home/linuxbrew/.linuxbrew ]; then
  eval "$(/home/linuxbrew/.linuxbrew/bin/brew shellenv)"
fi

# mise — runtime/tool version manager. Installs live under the home mount.
if command -v mise >/dev/null 2>&1; then
  eval "$(mise activate zsh)"
fi

# starship prompt.
if command -v starship >/dev/null 2>&1; then
  eval "$(starship init zsh)"
fi

# History.
HISTFILE="$HOME/.zsh_history"
HISTSIZE=50000
SAVEHIST=50000
setopt INC_APPEND_HISTORY SHARE_HISTORY HIST_IGNORE_DUPS

alias ll='ls -alh'
alias g=git

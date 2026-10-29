# tmux driver for bqt against the emulator. Source it: . .claude/skills/run-bqt-emulator/drv.sh
REPO=$(git rev-parse --show-toplevel)
E="$REPO/.scratch/emu"
S=bqt

# start [cols] [rows]: launch bqt in a fresh detached session
start() {
  tmux kill-session -t $S 2>/dev/null
  tmux new-session -d -s $S -x "${1:-120}" -y "${2:-36}" \
    -e BQT_CONFIG="$E/config.toml" -e BQT_BIGQUERY_ENDPOINT=http://localhost:9050 -e TERM=xterm-256color \
    "cd '$E' && ./bqt; echo EXITED=\$?; sleep 600"
  waitfor "layout:"
}
k() { tmux send-keys -t $S "$@"; sleep 0.4; }          # keys by name: k C-w l, k Enter, k Escape
lit() { tmux send-keys -t $S -l -- "$1"; sleep 0.4; }   # literal text
cap() { tmux capture-pane -t $S -p; }                   # the screen as text
size() { tmux resize-window -t $S -x "$1" -y "$2"; sleep 0.6; }
waitfor() { for _ in $(seq 1 50); do cap | grep -qF -- "$1" && return 0; sleep 0.2; done; echo "TIMEOUT waiting for: $1"; return 1; }
# mouse, as SGR sequences (turn it on first: palette "Mouse: on/off"); x and y are 1-based cells
click() { tmux send-keys -t $S -l $'\e'"[<0;$1;$2M"; tmux send-keys -t $S -l $'\e'"[<0;$1;$2m"; sleep 0.4; }
wheel() { tmux send-keys -t $S -l $'\e'"[<$1;$2;$3M"; sleep 0.3; }   # 64 up, 65 down, 66/67 sideways
mouseflags() { tmux display -p -t $S 'mouse_any=#{mouse_any_flag} sgr=#{mouse_sgr_flag} alt=#{alternate_on}'; }
quit() { k C-q; tmux kill-session -t $S 2>/dev/null || true; }

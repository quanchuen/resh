# Troubleshooting

## First help

Run RESH doctor to detect common issues:
```sh
reshctl doctor
```  

## Restarting RESH daemon

Sometimes restarting RESH daemon can help:
```sh
resh-daemon-restart
```

You can also start and stop RESH daemon with:
```sh
resh-daemon-start
resh-daemon-stop
```

:warning: You will get error messages in your shell when RESH daemon is not running.

## Recorded history

Your RESH history is saved in a SQLite database in one of:
- `~/.local/share/resh/history.db`
- `$XDG_DATA_HOME/resh/history.db`

Display the most recent commands using:

```sh
sqlite3 ~/.local/share/resh/history.db "
  SELECT r.time, p.value AS pwd, r.exit_code, c.value AS cmdline
  FROM records r
  JOIN strings c ON c.id = r.cmd
  JOIN strings p ON p.id = r.pwd
  ORDER BY r.id DESC LIMIT 20"
```

ℹ️ You will need `sqlite3` installed.

Older versions of RESH saved history in `history.reshjson` (one JSON record per line).
Updating RESH moves this history into the database (`resh-install-utils migrate-all` does it during installation).
If that did not happen, RESH daemon does it on its next start. History is only moved once.
The `history.reshjson` file is left unchanged as a backup and is no longer written to.
You can delete it once you have checked that your history is in the database.

## Configuration

RESH config is read from one of:
- `~/.config/resh.toml` 
- `$XDG_CONFIG_HOME/resh.toml`

## Logs

Logs can be useful for troubleshooting issues.

Find RESH logs in one of:
- `~/.local/share/resh/log.json`
- `$XDG_DATA_HOME/resh/log.json`

### Log verbosity

Get more detailed logs by setting `LogLevel = "debug"` in [RESH config](#configuration).  
Restart RESH daemon for the config change to take effect: `resh-daemon-restart`

## Common problems

### Using RESH with bash on macOS

ℹ️ It is recommended to use zsh on macOS.

MacOS comes with really old bash (`bash 3.2`).  
Update it using: `brew install bash`

On macOS, bash shell does not load `~/.bashrc` because every shell runs as login shell.  
Fix it by running: `echo '[ -f ~/.bashrc ] && . ~/.bashrc' >> ~/.bash_profile`

## GitHub issues

Problem persists? [Create an issue ⇗](https://github.com/curusarn/resh/issues)

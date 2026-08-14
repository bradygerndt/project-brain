# Backup and restore

Every instance's data — `memory.sqlite` (+ its WAL files), the `data/vectors/` LanceDB dataset,
and any `data/artifacts/<id>/<filename>` blobs — lives in one Docker volume,
`brain-<name>-data`. `brain backup`/`brain restore` tar that whole volume rather than
understanding SQLite/LanceDB internals: one portable file, and nothing to keep in sync with
future schema changes. The shared `brain-hf-cache` volume (embedding-model weights) is
deliberately *not* included — it's a re-downloadable cache, not instance data.

## Back up an instance

```bash
brain backup home                    # writes ./brain-home-<timestamp>.tar.gz
brain backup home ~/backups/home.tar.gz   # or pick the path yourself
```

This runs live by default — it does **not** stop the instance first, and there's no `--live`
flag to opt into that; live is the only mode. `memory_set` already writes SQLite and LanceDB via
a fire-and-forget update, so the two stores are already only eventually-consistent with each
other during normal operation — a live volume tar is no worse than that existing steady state.
If you want a belt-and-suspenders guarantee against a mid-write torn file, `brain stop home`
first; otherwise just run the backup.

The first run pulls a small `alpine` image (used to read the volume and write the tarball) if
it isn't already local — a one-time delay, cached after that.

## Restore into an instance

Restore only repopulates a volume — it assumes `<name>` is already registered via `brain add`
(ports and image are host-specific and could conflict on a different machine anyway, so restore
doesn't try to recreate the registry entry):

```bash
brain add home 3579 3580     # if not already registered on this machine
brain restore home ./brain-home-20260101-120000.tar.gz
```

Two refusals guard against surprises:

- **Refuses while the instance is running** — its volume is in active use (open SQLite handles,
  in-flight writes). Stop it first: `brain stop home`.
- **Refuses onto a non-empty volume** unless you pass `--force` — restore isn't a silent-clobber
  operation:

  ```bash
  brain restore home ./brain-home-20260101-120000.tar.gz --force
  ```

After a successful restore, start the instance normally: `brain start home`.

## Remote instances

`backup`/`restore` refuse on a [remote-reference instance](../README.md#remote-instances) (one
registered with `--host`) with the same "not managed by this brain" error as `start`/`update` —
there's no local Docker volume to tar or restore into, since the instance actually lives (and is
backed up, if at all) on whatever machine manages it.

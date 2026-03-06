# 11 — Scheduling System

## 1. Overview

The scheduling system adds cron-like timed execution to Golem. An agent (or a
user through the agent) creates schedules that associate a cron expression with
a prompt, a target channel, and a description. A background tick loop evaluates
which schedules are due, spins up an ephemeral one-shot session for each, runs
the prompt through the LLM, and delivers the response to the target channel.

Key files:

| File | Role |
|------|------|
| `internal/scheduler/schedule.go` | `Schedule` data model |
| `internal/scheduler/store.go` | JSON file persistence |
| `internal/scheduler/scheduler.go` | Tick loop, fire logic, `SessionFactory` interface |
| `internal/tools/builtin/schedule_tool.go` | `schedule_add`, `schedule_list`, `schedule_remove` tools |
| `internal/app/app.go` | Wiring: store init, factory creation, scheduler startup |

## 2. Schedule Model

A `Schedule` carries a UUID v4 identifier, a cron expression, the prompt text
sent to the LLM when the schedule fires, a channel name and channel ID
indicating where the response should be delivered, and an optional human-readable
description. It also records a creation timestamp, a last-fired timestamp, and
an enabled flag.

The cron expression supports standard 5-field syntax
(`minute hour dom month dow`), robfig descriptors (`@daily`, `@hourly`,
`@every 30m`), and `CRON_TZ=` prefixes. It is parsed by `robfig/cron/v3` with
the flags `Minute | Hour | Dom | Month | Dow | Descriptor`.

The creation time serves as the initial reference for the first `Next()`
calculation when the schedule has never fired; after each fire, `LastFiredAt`
takes over as the reference. Disabled schedules are skipped during tick
evaluation.

Source: `internal/scheduler/schedule.go`

## 3. Store

The store provides JSON file persistence with an in-memory cache guarded by a
`sync.Mutex`. It is created per-agent at `~/.golem/agents/<name>/schedules.json`.

`NewStore(path)` creates the store and `Load()` reads the JSON file into memory.
If the file does not exist, the store starts empty. `Add` validates the cron
expression, creates a `Schedule` with a new UUID, appends it to the in-memory
slice, and persists; if the write fails it rolls back the append. `Remove` finds
the schedule by ID, splices it out, and persists, returning an error if not
found. `List` returns a defensive copy of all schedules. `Get` returns a single
schedule by ID. `UpdateLastFired` sets `LastFiredAt` and persists on a
best-effort basis.

All writes go through `saveLocked()`, which marshals to JSON with indentation,
writes to a `.tmp` file, then atomically renames over the real path. This avoids
partial writes on crash. There is no OS-level file lock (`flock`); concurrency
is handled exclusively by the in-process mutex.

Source: `internal/scheduler/store.go`

## 4. Scheduler

The `Scheduler` struct holds a reference to the store, a map of channel names
to `Channel` implementations, a `SessionFactory` for creating ephemeral
sessions, a logger, a cron parser, and a cache of parsed cron expressions keyed
by schedule ID.

Source: `internal/scheduler/scheduler.go`

### Tick loop

`Run(ctx)` starts a `time.Ticker` at 60-second intervals and blocks until the
context is cancelled, performing an immediate tick on startup.

Each tick snapshots the current time, fetches all schedules from the store, and
calls `rebuildCache()` to parse cron expressions for any schedules added since
the last tick. It then iterates over all enabled schedules that have a cached
cron entry, picks a reference time (`LastFiredAt` if non-zero, otherwise
`CreatedAt`), computes `Next(ref)`, and fires any schedule whose next time is
at or before the current moment.

### Firing

When a schedule fires, the scheduler constructs a tape path
(`sched-<id[:8]>-<timestamp>.jsonl`) and builds an `IncomingMessage` with
`ChannelName` set to `"scheduler"`, `SenderName` set to
`"Scheduled: <description>"`, and the schedule's prompt as `Text`. It then
calls `factory.HandleScheduledPrompt` to run the prompt through an ephemeral
session, looks up the target channel by name, and sends the LLM response (or an
error notification on failure) via `ch.Send()`. Finally it updates
`LastFiredAt` to the current time.

### Cron cache management

`rebuildCache()` is called on construction and at each tick; it is idempotent
and only parses expressions not already cached. `AddToCache` is called by the
`schedule_add` tool after a new schedule is persisted so the next tick picks it
up immediately. `InvalidateCache` is called by `schedule_remove` after deletion.

## 5. Session Factory

The `SessionFactory` interface decouples the scheduler from session
construction. It declares a single method, `HandleScheduledPrompt`, which takes
a context, a tape path, and an `IncomingMessage`, and returns the LLM's text
response or an error.

The production implementation is `appSessionFactory` in `internal/app/app.go`.
For each fire it creates a fresh tape store under the configured tape directory,
a context strategy from config, a hooks bus with a logging hook named
`"scheduler"`, and a new tools registry via a stored `toolFactory` closure that
gives the ephemeral session the same tools as regular sessions (including the
schedule tools themselves). It then builds an `agent.Session` and calls
`HandleInput`, which runs the full ReAct loop and returns the final text
response.

The factory is wired during app startup: if the agent has a name (and therefore
a `SchedStore`), an `appSessionFactory` is created and passed to
`scheduler.New()`, which is then started in a goroutine. Because the
`toolFactory` closure includes schedule tools, scheduled sessions can themselves
create, list, or remove schedules.

## 6. Schedule Tools

`internal/tools/builtin/schedule_tool.go` exposes three tools to the LLM. All
three are registered with `Expand()` so they appear in the tool list without
needing an explicit mention in the system prompt.

**schedule_add** takes a cron expression, prompt, channel name, and channel ID
(all required), plus an optional description. It validates all required fields,
calls `store.Add()`, then calls `sched.AddToCache()` to hot-load the new
schedule. At registration time the `sched` pointer is nil because the
`Scheduler` instance does not exist yet, so `AddToCache` is a no-op in that
case; schedules created this way are still picked up on the next tick via
`rebuildCache()`.

**schedule_list** takes no parameters. It calls `store.List()` and formats each
schedule with its ID, description, cron expression, target, status, creation
time, and last fire time.

**schedule_remove** takes an ID. It calls `store.Remove(id)`, then
`sched.InvalidateCache(id)` to purge the cron cache entry. The same
nil-scheduler caveat applies.

## 7. Current Gaps

1. **No enable/disable tool.** The `Schedule` struct has an `Enabled` field,
   but there is no `schedule_enable` or `schedule_disable` tool. The only way
   to disable a schedule is to remove it.

2. **Scheduler pointer is nil in tools.** The schedule tools are constructed
   with a nil scheduler pointer because the `Scheduler` is not yet created at
   registration time. This means `AddToCache` / `InvalidateCache` calls are
   skipped; new or removed schedules are only reflected at the next
   `rebuildCache()` in the tick loop (up to 60 seconds delay). A
   post-construction setter or lazy reference would fix this.

3. **No concurrency guard on cache.** `cronCache` is a plain map accessed by
   both the tick goroutine and tool goroutines (via `AddToCache` /
   `InvalidateCache`). If the scheduler pointer were non-nil in tools, this
   would be a data race. Currently safe only because the pointer is always nil.

4. **Single-fire semantics on overdue.** If a schedule was overdue by multiple
   periods (e.g. hourly schedule, last fired 5 hours ago), `Next(ref)` returns
   only the first overdue time, so only one fire occurs per tick. Subsequent
   fires happen on subsequent ticks (one per minute). This is intentional but
   may surprise users expecting catch-up behavior.

5. **No per-schedule timezone.** Timezone is supported in the cron expression
   via `CRON_TZ=` prefix, but there is no dedicated field. Users must remember
   the prefix syntax.

6. **Atomic write but no file lock.** `saveLocked()` uses rename-over for
   atomicity, but there is no `flock`. If multiple Golem processes share the
   same agent directory, schedules could be lost. Currently not an issue because
   a single process owns each agent.

7. **No maximum schedule count.** There is no cap on the number of schedules.
   A misbehaving LLM could create unbounded schedules.

8. **Tape files accumulate.** Each fire creates a new tape file
   (`sched-<id>-<ts>.jsonl`) with no cleanup mechanism.

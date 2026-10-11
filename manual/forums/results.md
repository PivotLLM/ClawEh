# Forum results

When a forum run ends, the launching agent is notified, reads the result, and every message of the run stays on disk in the agent's workspace.

## The notice

When a run ends (completed, incomplete, failed or cancelled), the agent that
launched it, say Alice, gets a notice in her conversation:

```
[System: forum] Forum design-review (<id>) run 1 finished: completed.
```

A run that did not complete also gives the reason, such as "finished:
incomplete (deadline)." or "finished: failed (attempts_exhausted).".

Alice then answers, usually by reading the results and summarising them.
Her answer goes to:

1. the chat she launched the forum from (the WebUI chat included);
2. her [Default Channel](../agents/creating.md#default-channel) if that chat is no longer known
   (the run ended after a restart) or cannot be reached (for example a WebUI
   chat with no browser open);
3. nowhere, if she has no default channel. It still stays in her conversation.

If the notice cannot be delivered, or the results cannot be written, ClawEh
tries again every hour, up to five times per start of the service, and then
gives up with a warning in the log.

## What the agent gets back

`forum_results` returns, for the latest run or an earlier one by number,
each output of the result layers (in the templates, the council's verdict
or the writing forum's final text):

- who wrote it, which layer and round, its size, and the file holding it;
- its text, up to 4,000 characters per output and 16,000 characters for all
  outputs together. A longer output is cut and points to its file, and
  outputs beyond the total are listed without text;
- the path of the run's transcript.

While a run is still going, or after it stopped, `forum_results` returns
what has been published so far. If the result layers produced nothing (a run
that failed early), the outputs of the other layers are listed instead.

For long work such as a book chapter, ask the agent to end the forum with a
short summary layer. It then gets the summary in full and opens the long
outputs only when it needs them.

## Where the files are

Everything is in the launching agent's workspace (by default
`<CLAW_HOME>/agents/<agent>/`):

```
forums/<id>/
  forum-meta.json       which agent the forum belongs to
  forum.json            the setup, as it stands now
  runs/<n>/             run n (1, 2, 3, ...); a later run never changes it
    forum.json          the setup this run used
    transcript.md       the readable transcript, written as turns are published
    result.json         the result, once the run has ended
    layers/<layer>/...  every message sent, every reply, every output
    commits/            the run's log, used to resume after a restart
```

Nothing is deleted until the agent deletes the forum.

The agent's [file tools](../agents/files-and-mounts.md) can read its `forums/` folder but cannot write to it,
and cannot read another agent's forums.

## The transcript

`transcript.md` is the run as a readable document: a heading with the forum
and run, then each published output, quoted in a block, and the moderator's
public decisions. It never contains a participant's private instructions,
rejected replies or private messages.

To follow a running forum from a shell on the ClawEh machine:

```
tail -f <workspace>/forums/<id>/runs/<n>/transcript.md
```

## Anonymous review

A forum can show a layer's outputs to readers without their authors. The
[council](templates.md#council) template does this: when the members review
each other's answers, they see "Response A", "Response B", and so on, never
who wrote them, and never their own answer.

In plain terms:

- The letters are fixed by the authors' order in that layer, so they are the
  same for every reader and after a restart.
- A reader never sees its own output among the anonymous ones.
- Anyone else who reads that layer normally, such as the council's chair,
  sees the author with the letter, "Bob (Response A)", so the reviews can be
  matched to the answers.
- The transcript and the results keep the real names, with the letter next
  to each author.
- Because the launching agent can read the forum's files, neither it nor a
  clone of it can take part in a forum that uses anonymous review. The
  agent's check refuses such a setup, and any other setup in which a reader
  could learn the authors another way.

## Backups

The nightly [backup](../admin/backup.md) includes every agent's `forums/` folder: each forum's
setup and every run's files. Lock files and the temporary files of a write
in progress are left out. A forum that was running during the backup is
recovered after a restore like a run interrupted by a restart, and its
transcript is rebuilt.

An agent whose workspace is set outside the agents folder is not fully
covered: back its `forums/` folder up yourself.

See also [Forums](index.md) and [Running a forum](running.md).

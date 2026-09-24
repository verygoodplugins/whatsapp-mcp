---
name: whatsapp-video
description: Watch a video that arrived in a WhatsApp chat. Use whenever a WhatsApp message contains a video and the question is about what happens in it — what was filmed, what someone says, whether it shows a given thing — including "mirá el video que me mandó X", "¿qué dice el video del grupo?", or a summary of a chat where a video carries the point. The MCP server only reaches the first frame of a video; this fetches the file and hands it to the viewvideo skill, which extracts scene keyframes plus an audio transcript.
---

# Watching a WhatsApp video

`view_media` renders the first frame of a video and nothing else. A first frame
answers "what was sent"; it does not answer "what happens in it". For that the
file has to come down to disk and go through `viewvideo`, which extracts
scene-aware keyframes and an audio transcript that Claude can read directly.

This fork is read-only. Nothing here sends, reacts, or marks anything as read.

## Steps

1. **Find the message.** `list_messages` or `get_message_context` on the chat.
   A video message has media, and its `content` is usually empty or just a
   caption. Keep both its `id` and the chat's `jid` — every media tool needs
   the pair.

2. **Download it.** `download_media(message_id, chat_jid)` returns a local file
   path. Use that path; do not guess where the bridge stores media. If it fails,
   say so and stop: WhatsApp expires media server-side after a few weeks, so an
   old video is often simply gone, and no retry brings it back.

3. **Watch it.** Invoke the `viewvideo` skill on that path and follow it. It
   handles the extraction and tells Claude what to read. Do not hand it a
   WhatsApp URL — the media is encrypted, only the downloaded file works.

4. **Answer from what the frames and the transcript actually show.** Say which
   of the two something came from when it matters: a transcript can mishear, and
   a keyframe can miss what happened between frames. If the video is silent, say
   that rather than reporting an empty transcript as if nobody spoke.

## When not to use this

- **A photo** — `view_media` returns the image itself. This skill is for video.
- **A voice note** — `transcribe_audio` runs whisper.cpp locally and writes the
  transcript into the message, where `list_messages` will find it afterwards.
- **Only needing to know a video exists** — `list_messages` already shows that,
  and `view_media` gives the first frame. Downloading and extracting a long
  video is slow; don't do it to answer a question a frame answers.

## Groups

A video in a group is media other people sent. It is readable here because the
account owner is in that group, which is not the same as those people agreeing
to have it analysed. Answer the question that was asked about it; don't
volunteer an inventory of what else the video reveals about the people in it.

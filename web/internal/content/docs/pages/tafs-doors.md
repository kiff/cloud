# Doors: an email address or a Slack channel for a taf

A **door** gives a taf its own email address or Slack channel, and the answer goes back the same way.
For whoever manages Cards and wants work to reach a taf from outside the app.
Add one in the taf's **Setup › Starts when**. Anyone can knock; only people you pair can give work.

## The rules

- A **paired sender** gives work like a chat message: under the taf's Card, holds still wait for
  you, all in the Record. Your sign-in email is paired from the start.
- A sender counts as paired only when the door proves it: email's sender check must pass, and
  Slack gives a member ID.
- Anyone else **knocks**. You answer **Do it** (a run from your words, their message as
  information), **Pair** (from their next message) or **Ignore**.
- The door writes back only to whoever wrote, as an AI assistant.

## Slack

Connect the taf's Slack bot in **Connect apps**, then add a Slack door on one of its channels.
The taf answers in the thread with the bot's send-message action, through KIFF inside its Card.
tafs holds no Slack sign-in.

## Limits

- 20 messages a day from you and paired senders, 10 from anyone else; then they're dropped and
  you're told how many.
- Text only, up to 20,000 characters; attachments aren't opened.
- A message older than 48 hours starts nothing.

Only people who manage the taf see its door conversations and knocks. Only someone who manages
Cards turns doors on, pairs senders and answers knocks.

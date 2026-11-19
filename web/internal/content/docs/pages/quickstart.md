# Quickstart: give an agent a Card

Give one agent a [KIFF Card](/docs/kiff-cards) in the dashboard. No code.
For anyone with an agent that can use a remote MCP server, such as Claude Code or Codex.
Connect a tool, issue the Card, point the agent at KIFF, and answer its first held call.

You need an account at [app.kiff.dev](https://app.kiff.dev) and a remote
MCP server at a public HTTPS address. Agent in your own code? See
[Connect an agent](/docs/connect-an-agent).

## 1. Say who the account is for

At first sign-in, choose **For me** (My Cards, My agents, Connected tools,
Needs you, Activity) or **For a company** (the full workspace). Change it
in **Settings → Dashboard view**.

## 2. Connect a tool

On **Connected tools**, choose **Connect a tool**. Enter the server's URL
and, if needed, its bearer credential. Pick the tool, the name the agent
sees, and its amount field if it has one. KIFF stores the credential
encrypted. To keep the key out of KIFF, see
[Keep tool keys out of KIFF](/docs/tool-permits).

## 3. Give the agent a Card

Add the agent on **My agents**. On the connected tool, choose **Give an
agent a Card**: pick the agent, set how much and how often, choose what
happens past that (**Hold it for an approver** or **Refuse it**), then
**Issue the Card**.

![An issued Card: 100 EUR left of 100 EUR a day, with Limits, Extra room, Share and Revoke.](/static/img/docs/card-issued.jpg)

## 4. Point the agent at KIFF

Open the agent on **My agents**, then its **Tools** tab. Choose **Claude
Code or Codex**, then **Create the key and config**. KIFF shows the key
once, inside this configuration:

```json
{
  "mcpServers": {
    "kiff": {
      "type": "http",
      "url": "https://mcp.kiff.dev/mcp",
      "headers": { "Authorization": "Bearer <the agent's key>" }
    }
  }
}
```

Keep the key out of anything you commit, and remove the agent's direct
access to the same tool.

## 5. Go over the limit once

Ask the agent to use the tool past its Card. The call is held: you get
an email and it appears on **Needs you**. Choose **Approve once**,
**Review extra room** or **Reject**. See
[Held calls and approvals](/docs/approvals).

Every call is on **Activity**. To stop the agent, revoke its key on
**Connections**, or revoke the Card.

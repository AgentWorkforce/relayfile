# Slack Event Subscription Rubric

The suite passes when Relayfile can represent every Slack event subscription
that the Slack app requests without requiring live network access.

- Public channel lifecycle events produce channel-shaped paths under `/slack/channels`.
- Private channel lifecycle events whose Slack names start with `group_` also produce channel-shaped paths.
- Deleted channel and group events are preserved as deletion semantics.
- Membership events remain channel metadata updates.
- Public channel, private channel, DM, and MPDM message events land under message paths.
- Team and user events land under `/slack/users`.
- The evaluator must not require a Slack MCP or Slack Web API call for fixture-backed cases.

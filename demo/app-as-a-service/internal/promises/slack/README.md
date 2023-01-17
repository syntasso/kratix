# Slack

This Promise provides Slack-Messages-as-a-Service. It has a single field: `.spec.message`, which is the message to be sent.

To install, run the following commands while targeting your Platform cluster:
```
kubectl apply -f https://raw.githubusercontent.com/syntasso/kratix-marketplace/main/slack/promise.yaml
```

This promise uses a [Slack hook](https://slack.com/intl/en-gb/help/articles/115005265063-Incoming-webhooks-for-Slack)
to send messages to a channel. To provide access to this hook create secret in `default` namespace called
`slack-channel-hook` with a `.data.url` field containing the hook. You can create it using
the following command (ensure you have `SLACK_HOOK_URL` env var exported):
```
kubectl --namespace default create secret generic \
  slack-channel-hook --from-literal=url=${SLACK_HOOK_URL}
```

To verify that the Promise is installed, run the following on your Platform cluster:
```
$ kubectl get promises.platform.kratix.io
NAME        AGE
slack       1m
```

To create a slack message, run the following command while targeting the Platform cluster:
```
kubectl apply -f https://raw.githubusercontent.com/syntasso/kratix-marketplace/main/slack/resource-request.yaml
```

You should see a slack message appear shortly afterwards.

### Deletion
As we've created the `slack-channel-hook` `secret` outside of the usual Promise workflow, when deleteing the resource request we will need to delete this secret manaually:
```
kubectl delete secret slack-channel-hook
```


## Development

For development see [README.md](./internal/README.md)

## Questions? Feedback?

We are always looking for ways to improve Kratix and the Marketplace. If you run into issues or have ideas for us, please let us know. Feel free to [open an issue](https://github.com/syntasso/kratix-marketplace/issues/new/choose) or [put time on our calendar](https://www.syntasso.io/contact-us). We'd love to hear from you.

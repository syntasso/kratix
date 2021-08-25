# Jenkins Promise

0. Set up Kratix (Insert link)
1. Deploy the Jenkins Promise `kubectl apply -f jenkins-promise.yaml` 
2. Ask for a Jenkins instance `kubectl apply -f jenkins-resource-request.yaml` 
3. Switch to the Worker cluster running your Jenkins instance `kubectl config use-context kind-worker`
4. Get username: `kubectl get secret jenkins-operator-credentials-example -o 'jsonpath={.data.user}' | base64 -d`
5. Get password: `kubectl get secret jenkins-operator-credentials-example -o 'jsonpath={.data.password}' | base64 -d`
6. Expose Jenkins to your host so you can connect `kubectl port-forward jenkins-<cr_name> 8080:8080`
7. Open a browser at `http://localhost:8080`
8. Enter username and password.
9. Enjoy Jenkins 

# Warning
Currenlty the request pipeline is only configred to stamp one instance of Jenkins per cluster due to the name clash.

Takes 2 or 3 mintues to move to 'Running' state.  
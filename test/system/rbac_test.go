package system_test

//			roleName := strings.Trim(platform.kubectl("get", "role", "-l",
//				userPermissionRoleLabels(bashPromiseName, "resource", "configure", "first-configure"),
//				"-o=jsonpath='{.items[0].metadata.name}'"), "'")
//			roleCreationTimestamp := platform.kubectl("get", "role", roleName, "-o=jsonpath='{.metadata.creationTimestamp}'")
//
//			bindingName := strings.Trim(platform.kubectl("get", "rolebinding", "-l",
//				userPermissionRoleLabels(bashPromiseName, "resource", "configure", "first-configure"),
//				"-o=jsonpath='{.items[0].metadata.name}'"), "'")
//			bindingCreationTimestamp := platform.kubectl("get", "rolebinding", bindingName, "-o=jsonpath='{.metadata.creationTimestamp}'")
//
//			specificNamespaceClusterRoleName := strings.Trim(platform.kubectl("get", "ClusterRole", "-l",
//				userPermissionClusterRoleLabels(bashPromiseName, "resource", "configure", "first-configure", "pipeline-perms-ns-"+bashPromiseName),
//				"-o=jsonpath='{.items[0].metadata.name}'"), "'")
//			specificNamespaceClusterRoleCreationTimestamp := platform.kubectl("get", "ClusterRole", specificNamespaceClusterRoleName, "-o=jsonpath='{.metadata.creationTimestamp}'")
//
//			allNamespaceClusterRoleName := strings.Trim(platform.kubectl("get", "ClusterRole", "-l",
//				userPermissionClusterRoleLabels(bashPromiseName, "resource", "configure", "first-configure", "kratix_all_namespaces"),
//				"-o=jsonpath='{.items[0].metadata.name}'"), "'")
//			allNamespaceClusterRoleCreationTimestamp := platform.kubectl("get", "ClusterRole", allNamespaceClusterRoleName, "-o=jsonpath='{.metadata.creationTimestamp}'")
//
//			By("updating a promise does not recreate rbac", func() {
//				Expect(platform.kubectl("get", "role", roleName, "-o=jsonpath='{.metadata.creationTimestamp}'")).To(Equal(roleCreationTimestamp))
//				Expect(platform.kubectl("get", "rolebinding", bindingName, "-o=jsonpath='{.metadata.creationTimestamp}'")).To(Equal(bindingCreationTimestamp))
//				Expect(platform.kubectl("get", "clusterrole", specificNamespaceClusterRoleName, "-o=jsonpath='{.metadata.creationTimestamp}'")).To(Equal(specificNamespaceClusterRoleCreationTimestamp))
//				Expect(platform.kubectl("get", "clusterrole", allNamespaceClusterRoleName, "-o=jsonpath='{.metadata.creationTimestamp}'")).To(Equal(allNamespaceClusterRoleCreationTimestamp))
//			})

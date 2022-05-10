package awsplacementgroup

// TODO(damdo): uncomment when Machine's ProviderSpec supports Groups
// machineToAWSPlacementGroup maps a Machine to an AWSPlacementGroup
// provided that the Machine references one.
// func machineToAWSPlacementGroup(r *AWSPlacementGroupReconciler) func(client.Object) []reconcile.Request {
// 	return func(obj client.Object) []reconcile.Request {
// 		machine, ok := obj.(*machinev1beta1.Machine)
// 		if !ok {
// 			panic(fmt.Sprintf("expected type *machinev1beta1.Machine, got %T", obj))
// 		}

// 		awsProviderSpec, err := awsmachine.ProviderSpecFromRawExtension(machine.Spec.ProviderSpec.Value)
// 		if err != nil {
// 			// Ignore the Machine if there is an error while deconding its ProviderSpec.
// 			return []reconcile.Request{}
// 		}

// 		if awsProviderSpec.Placement.Group.Name == "" {
// 			// Ignore the Machine if it doesn't reference an AWSPlacementGroup.
// 			return []reconcile.Request{}
// 		}

// 		// Return a reconcile Request with the name and namespace of the
// 		// AWSPlacementGroup referenced by the Machine.
// 		return []reconcile.Request{
// 			{
// 				NamespacedName: types.NamespacedName{
// 					Name:      awsProviderSpec.Placement.Group.Name,
// 					Namespace: obj.GetNamespace(),
// 				},
// 			},
// 		}
// 	}
// }

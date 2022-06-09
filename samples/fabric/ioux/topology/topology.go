/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package topology

import (
	"github.com/hyperledger-labs/fabric-smart-client/integration/nwo/api"
	"github.com/hyperledger-labs/fabric-smart-client/integration/nwo/fabric"
	"github.com/hyperledger-labs/fabric-smart-client/integration/nwo/fsc"
	"github.com/hyperledger-labs/fabric-smart-client/samples/fabric/ioux/views"
)

func Topology() []api.Topology {
	// Define a Fabric topology with:
	// 1. Four organization: Org1, Org2, Org3, and Org4
	// 2. A namespace whose changes can be endorsed by Org1.
	fabricTopology := fabric.NewDefaultTopology()
	fabricTopology.AddOrganizationsByName("Org1", "Org2", "Org3", "Org4")
	fabricTopology.SetNamespaceApproverOrgs("Org1")
	fabricTopology.AddNamespaceWithUnanimity("ioux", "Org1")
	fabricTopology.EnableGRPCLogging()
	fabricTopology.EnableLogPeersToFile()
	fabricTopology.EnableLogOrderersToFile()
	fabricTopology.SetLogging("info", "")

	// Define an FSC topology with 3 FCS nodes.
	// One for the approver, one for the borrower, and one for the lender.
	fscTopology := fsc.NewTopology()
	fscTopology.SetLogging("debug", "")
	fscTopology.EnableLogToFile()
	fscTopology.EnablePrometheusMetrics()

	// Add the approver FSC node.
	approver := fscTopology.AddNodeByName("approver")
	// This option equips the approver's FSC node with an identity belonging to Org1.
	// Therefore, the approver is an endorser of the Fabric namespace we defined above.
	approver.AddOptions(fabric.WithOrganization("Org1"))
	approver.RegisterResponder(&views.ApproverView{}, &views.CreateIOUView{})
	approver.RegisterResponder(&views.ApproverView{}, &views.UpdateIOUView{})

	// Add the Alice's FSC node
	alice := fscTopology.AddNodeByName("alice")
	alice.AddOptions(fabric.WithOrganization("Org2"))
	alice.RegisterResponder(&views.CreateIOUResponderView{}, &views.CreateIOUView{})
	alice.RegisterResponder(&views.UpdateIOUResponderView{}, &views.UpdateIOUView{})
	alice.RegisterViewFactory("create", &views.CreateIOUViewFactory{})
	alice.RegisterViewFactory("update", &views.UpdateIOUViewFactory{})
	alice.RegisterViewFactory("query", &views.QueryViewFactory{})

	// Add the Bob's FSC node
	bob := fscTopology.AddNodeByName("bob")
	bob.AddOptions(fabric.WithOrganization("Org3"))
	bob.RegisterResponder(&views.CreateIOUResponderView{}, &views.CreateIOUView{})
	bob.RegisterResponder(&views.UpdateIOUResponderView{}, &views.UpdateIOUView{})
	bob.RegisterViewFactory("create", &views.CreateIOUViewFactory{})
	bob.RegisterViewFactory("update", &views.UpdateIOUViewFactory{})
	bob.RegisterViewFactory("query", &views.QueryViewFactory{})

	// Add the Charlie's FSC node
	charlie := fscTopology.AddNodeByName("charlie")
	charlie.AddOptions(fabric.WithOrganization("Org4"))
	charlie.RegisterResponder(&views.CreateIOUResponderView{}, &views.CreateIOUView{})
	charlie.RegisterResponder(&views.UpdateIOUResponderView{}, &views.UpdateIOUView{})
	charlie.RegisterViewFactory("create", &views.CreateIOUViewFactory{})
	charlie.RegisterViewFactory("update", &views.UpdateIOUViewFactory{})
	charlie.RegisterViewFactory("query", &views.QueryViewFactory{})

	// Monitoring
	//monitoringTopology := monitoring.NewTopology()
	//monitoringTopology.EnableHyperledgerExplorer()
	//monitoringTopology.EnablePrometheusGrafana()

	return []api.Topology{
		fabricTopology,
		fscTopology,
		//monitoringTopology,
	}
}

package applicationset

import (
	"context"
	"fmt"
	"sort"

	appsetclientset "github.com/argoproj/argo-cd/v2/applicationset/clientset"
	"github.com/argoproj/argo-cd/v2/pkg/apiclient/application"
	"github.com/argoproj/argo-cd/v2/pkg/apiclient/applicationset"
	"github.com/argoproj/argo-cd/v2/server/rbacpolicy"
	"github.com/argoproj/argo-cd/v2/util/argo"
	"github.com/argoproj/argo-cd/v2/util/db"
	"github.com/argoproj/argo-cd/v2/util/rbac"
	"github.com/argoproj/gitops-engine/pkg/utils/kube"
	"github.com/argoproj/pkg/sync"
	"k8s.io/client-go/kubernetes"
)

type Server struct {
	ns              string
	kubeclientset   kubernetes.Interface
	appsetclientset appsetclientset.Interface
	kubectl         kube.Kubectl
	db              db.ArgoDB
	enf             *rbac.Enforcer
	projectLock     sync.KeyLock
}

// NewServer returns a new instance of the Application service
func NewServer(
	namespace string,
	kubeclientset kubernetes.Interface,
	appclientset appsetclientset.Interface,
	kubectl kube.Kubectl,
	projectLock sync.KeyLock,
) applicationset.ApplicationSetServiceServer {
	appInformer.AddEventHandler(appBroadcaster)
	s := &Server{
		ns:             namespace,
		appclientset:   appclientset,
		appLister:      appLister,
		appInformer:    appInformer,
		appBroadcaster: appBroadcaster,
		kubeclientset:  kubeclientset,
		cache:          cache,
		db:             db,
		repoClientset:  repoClientset,
		kubectl:        kubectl,
		enf:            enf,
		projectLock:    projectLock,
		auditLogger:    argo.NewAuditLogger(namespace, kubeclientset, "argocd-server"),
		settingsMgr:    settingsMgr,
		projInformer:   projInformer,
	}
	return s, s.GetAppResources
}

// List returns list of applications
func (s *Server) List(ctx context.Context, q *application.ApplicationQuery) (*appv1.ApplicationList, error) {
	labelsMap, err := labels.ConvertSelectorToLabelsMap(q.GetSelector())
	if err != nil {
		return nil, fmt.Errorf("error converting selector to labels map: %w", err)
	}
	apps, err := s.appLister.List(labelsMap.AsSelector())
	if err != nil {
		return nil, fmt.Errorf("error listing apps with selectors: %w", err)
	}
	newItems := make([]appv1.ApplicationSet, 0)
	for _, a := range apps {
		if s.enf.Enforce(ctx.Value("claims"), rbacpolicy.ResourceApplications, rbacpolicy.ActionGet, apputil.AppRBACName(*a)) {
			newItems = append(newItems, *a)
		}
	}
	if q.Name != nil {
		newItems, err = argoutil.FilterByName(newItems, *q.Name)
		if err != nil {
			return nil, fmt.Errorf("error filtering applications by name: %w", err)
		}
	}

	// Sort found applications by name
	sort.Slice(newItems, func(i, j int) bool {
		return newItems[i].Name < newItems[j].Name
	})

	appList := appv1.ApplicationList{
		ListMeta: metav1.ListMeta{
			ResourceVersion: s.appInformer.LastSyncResourceVersion(),
		},
		Items: newItems,
	}
	return &appList, nil
}

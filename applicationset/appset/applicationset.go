package appset

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	appsetclientset "github.com/argoproj/argo-cd/v2/applicationset/clientset"
	"github.com/argoproj/argo-cd/v2/pkg/apiclient/application"
	"github.com/argoproj/argo-cd/v2/pkg/apiclient/applicationset"
	"github.com/argoproj/argo-cd/v2/pkg/apis/applicationset/v1alpha1"
	"github.com/argoproj/argo-cd/v2/server/rbacpolicy"
	"github.com/argoproj/argo-cd/v2/util/argo"
	"github.com/argoproj/argo-cd/v2/util/db"
	"github.com/argoproj/argo-cd/v2/util/rbac"
	"github.com/argoproj/gitops-engine/pkg/utils/kube"
	"github.com/argoproj/pkg/sync"
	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

type Server struct {
	ns              string
	kubeclientset   kubernetes.Interface
	appsetclientset appsetclientset.Interface
	kubectl         kube.Kubectl
	db              db.ArgoDB
	enf             *rbac.Enforcer
	projectLock     sync.KeyLock
	indexer         cache.Indexer
}

// NewServer returns a new instance of the Application service
func NewServer(
	namespace string,
	kubeclientset kubernetes.Interface,
	appclientset appsetclientset.Interface,
	kubectl kube.Kubectl,
	projectLock sync.KeyLock,
) applicationset.ApplicationSetServiceServer {
	s := &Server{
		ns:              namespace,
		appsetclientset: appclientset,
		kubeclientset:   kubeclientset,
		db:              db,
		kubectl:         kubectl,
		enf:             enf,
		projectLock:     projectLock,
		indexer:         cache.Indexer,
	}
	return s
}

// List returns list of applications
func (s *Server) List(ctx context.Context, q *application.ApplicationQuery) (apps *v1alpha1.ApplicationSetList, error) {
	labelsMap, err := labels.ConvertSelectorToLabelsMap(q.GetSelector())
	if err != nil {
		return nil, fmt.Errorf("error converting selector to labels map: %w", err)
	}
	apps, err := cache.ListAll(s.indexer, labelsMap.AsSelector(), func(m interface{}) {
		apps.Items = append(apps.Items, m.(*v1alpha1.ApplicationSet))
	})

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

func (s *Server) Create(ctx context.Context, q *applicationset.ApplicationSetCreateRequest, opts ...grpc.CallOption) (*v1alpha1.ApplicationSet, error) {
	if q.GetApplicationset() == nil {
		return nil, fmt.Errorf("error creating application: application is nil in request")
	}
	if err := s.enf.EnforceErr(ctx.Value("claims"), rbacpolicy.ResourceApplications, rbacpolicy.ActionCreate, apputil.AppRBACName(*q.ApplicationSet)); err != nil {
		return nil, err
	}

	s.projectLock.RLock(q.Application.Spec.Project)
	defer s.projectLock.RUnlock(q.Application.Spec.Project)

	a := q.GetApplication()
	validate := true
	if q.Validate != nil {
		validate = *q.Validate
	}
	err := s.validateAndNormalizeApp(ctx, a, validate)
	if err != nil {
		return nil, fmt.Errorf("error while validating and normalizing app: %w", err)
	}
	created, err := s.appclientset.ArgoprojV1alpha1().Applications(s.ns).Create(ctx, a, metav1.CreateOptions{})
	if err == nil {
		s.logAppEvent(created, ctx, argo.EventReasonResourceCreated, "created application")
		s.waitSync(created)
		return created, nil
	}
	if !apierr.IsAlreadyExists(err) {
		return nil, fmt.Errorf("error creating application: %w", err)
	}
	// act idempotent if existing spec matches new spec
	existing, err := s.appLister.Get(a.Name)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unable to check existing application details: %v", err)
	}
	equalSpecs := reflect.DeepEqual(existing.Spec, a.Spec) &&
		reflect.DeepEqual(existing.Labels, a.Labels) &&
		reflect.DeepEqual(existing.Annotations, a.Annotations) &&
		reflect.DeepEqual(existing.Finalizers, a.Finalizers)

	if equalSpecs {
		return existing, nil
	}
	if q.Upsert == nil || !*q.Upsert {
		return nil, status.Errorf(codes.InvalidArgument, "existing application spec is different, use upsert flag to force update")
	}
	if err := s.enf.EnforceErr(ctx.Value("claims"), rbacpolicy.ResourceApplications, rbacpolicy.ActionUpdate, apputil.AppRBACName(*a)); err != nil {
		return nil, err
	}
	updated, err := s.updateApp(existing, a, ctx, true)
	if err != nil {
		return nil, fmt.Errorf("error updating application: %w", err)
	}
	return updated, nil
}

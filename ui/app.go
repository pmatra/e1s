package ui

import (
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/gdamore/tcell/v2"
	"github.com/keidarcy/e1s/api"
	"github.com/rivo/tview"
	"github.com/sirupsen/logrus"
)

const (
	L = tview.AlignLeft
	C = tview.AlignCenter
	R = tview.AlignRight
)

var logger *logrus.Logger

// Entity contains ECS resources to show
type Entity struct {
	cluster                 *types.Cluster
	service                 *types.Service
	task                    *types.Task
	container               *types.Container
	taskDefinition          *types.TaskDefinition
	events                  []types.ServiceEvent
	taskDefinitionRevisions api.TaskDefinitionRevision
	metrics                 *api.MetricsData
	autoScaling             *api.AutoScalingData
	entityName              string
}

type Option struct {
	// Read only mode indicator
	ReadOnly bool
	// Basic logger
	Logger *logrus.Logger
	// Reload resources every x second(s), -1 is stop auto refresh
	Refresh int
}

// tview App
type App struct {
	// tview Application
	*tview.Application
	// Info + table area pages UI for MainScreen
	*tview.Pages
	// Notice text UI in MainScreen footer
	Notice *Notice
	// MainScreen content UI
	MainScreen *tview.Flex
	// API client
	*api.Store
	// Option from cli args
	Option
	// Current screen item content
	Entity
	// Current page primary kind ex: cluster, service
	kind Kind
	// Current secondary kind like json, list
	secondaryKind Kind
	// Port forwarding ssm session Id
	sessions []*PortForwardingSession
	// Current primary kind table row index for auto refresh to keep row selected
	rowIndex int
}

func newApp(option Option) (*App, error) {
	store, err := api.NewStore(option.Logger)
	if err != nil {
		return nil, err
	}
	region := store.Config.Region
	if len(region) == 0 {
		region = "unknown"
	}
	app := tview.NewApplication()
	pages := tview.NewPages()
	footer := tview.NewFlex()

	notice := newNotice(app)
	footer.AddItem(notice, 0, 1, false)
	main := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(pages, 0, 2, true).
		AddItem(footer, 1, 1, false)

	return &App{
		Application:   app,
		Pages:         pages,
		Notice:        notice,
		MainScreen:    main,
		Store:         store,
		Option:        option,
		kind:          ClusterKind,
		secondaryKind: EmptyKind,
		Entity: Entity{
			cluster: &types.Cluster{
				ClusterName: aws.String("placeholder cluster"),
			},
			service: &types.Service{
				ServiceName: aws.String("placeholder service"),
			},
		},
	}, nil
}

var rowIndexMutex sync.Mutex

// Entry point of the app
func Start(option Option) error {
	logger = option.Logger
	logger.Debug(`
****************************************************************
**************** Started e1s
****************************************************************`)
	app, err := newApp(option)
	if err != nil {
		return err
	}

	app.initStyles()

	if err := app.start(); err != nil {
		return err
	}

	if err := app.Application.SetRoot(app.MainScreen, true).Run(); err != nil {
		return err
	}
	app.onClose()
	return nil
}

// Init basic tview styles
func (app App) initStyles() {
	tview.Styles.PrimitiveBackgroundColor = tcell.ColorBlack
	tview.Styles.ContrastBackgroundColor = tcell.ColorBlack
	tview.Styles.PrimaryTextColor = tcell.ColorWhite
	tview.Styles.BorderColor = tcell.ColorDarkCyan
}

// Add new page to app.Pages
func (app *App) addAppPage(page *tview.Flex) {
	pageName := app.kind.getAppPageName(app.getPageHandle())

	logger.WithFields(logrus.Fields{
		"Action":        "AppPage",
		"PageName":      pageName,
		"Kind":          app.kind.String(),
		"SecondaryKind": app.secondaryKind.String(),
		"Cluster":       *app.cluster.ClusterName,
		"Service":       *app.service.ServiceName,
		// "RowIndex":      app.rowIndex,
	}).Debug("AddPage app.Pages")

	app.Pages.AddPage(pageName, page, true, true)
}

// Switch app.Pages page
func (app *App) SwitchPage(reload bool) bool {
	pageName := app.kind.getAppPageName(app.getPageHandle())
	if app.Pages.HasPage(pageName) && app.Refresh < 0 && !reload {

		logger.WithFields(logrus.Fields{
			"Action":        "SwitchTo",
			"Kind":          app.kind.String(),
			"SecondaryKind": app.secondaryKind.String(),
			"PageName":      pageName,
			"Cluster":       *app.cluster.ClusterName,
			"Service":       *app.service.ServiceName,
			// "RowIndex":      app.rowIndex,
		}).Debug("SwitchToPage app.Pages")

		app.Pages.SwitchToPage(pageName)
		return true
	}
	return false
}

// Go back page based on current kind
func (app *App) back() {
	prevKind := app.kind.prevKind()
	app.kind = prevKind
	app.secondaryKind = EmptyKind
	pageName := prevKind.getAppPageName(app.getPageHandle())

	logger.WithFields(logrus.Fields{
		"Action":        "Back",
		"PageName":      pageName,
		"Kind":          app.kind.String(),
		"SecondaryKind": app.secondaryKind.String(),
		"Cluster":       *app.cluster.ClusterName,
		"Service":       *app.service.ServiceName,
		// "RowIndex":      app.rowIndex,
	}).Debug("Back app.Pages")

	app.Pages.SwitchToPage(pageName)
}

// Get page handler, cluster is empty, other is cluster arn
func (app *App) getPageHandle() string {
	name := ""
	if app.kind != ClusterKind {
		name = *app.cluster.ClusterArn
	}
	return name
}

func (app *App) start() error {
	err := app.showPrimaryKindPage(ClusterKind, false)

	if app.Option.Refresh > 0 {
		logger.Debugf("Auto refresh rate every %d seconds", app.Option.Refresh)
		ticker := time.NewTicker(time.Duration(app.Option.Refresh) * time.Second)

		go func() {
			for {
				<-ticker.C
				if app.secondaryKind == EmptyKind {
					app.showPrimaryKindPage(app.kind, true)
					app.Application.Draw()
				}
			}
		}()
	}
	return err
}

// Show Primary kind page
func (app *App) showPrimaryKindPage(k Kind, reload bool) error {
	var err error
	switch k {
	case ClusterKind:
		app.kind = ClusterKind
		err = app.showClustersPage(reload)
	case ServiceKind:
		app.kind = ServiceKind
		err = app.showServicesPage(reload)
	case TaskKind:
		app.kind = TaskKind
		err = app.showTasksPages(reload)
	case ContainerKind:
		app.kind = ContainerKind
		err = app.showContainersPage(reload)
	default:
		app.kind = ClusterKind
		err = app.showClustersPage(reload)
	}
	if err != nil {
		app.Notice.Error(err.Error())
		return err
	}
	if !reload {
		app.Notice.Infof("Viewing %s...", app.kind.String())
	} else {
		logger.Debugf("Reload in showPrimaryKindPage: %v", reload)
	}
	return nil
}

// E1s app close hook
func (app *App) onClose() {
	if len(app.sessions) != 0 {
		ids := []*string{}
		for _, s := range app.sessions {
			ids = append(ids, s.sessionId)
		}
		err := app.Store.TerminateSessions(ids)
		if err != nil {
			logger.Errorf("Failed to terminated port forwarding sessions err: %v", err)
		} else {
			logger.Debug("Terminated port forwarding session terminated")
		}
	}

	logger.Debug(`
**************** Exited e1s ************************************`)
}

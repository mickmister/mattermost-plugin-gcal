package mscalendar

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/jarcoal/httpmock"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-mscalendar/server/config"
	"github.com/mattermost/mattermost-plugin-mscalendar/server/mscalendar/mock_plugin_api"
	"github.com/mattermost/mattermost-plugin-mscalendar/server/remote"
	"github.com/mattermost/mattermost-plugin-mscalendar/server/remote/msgraph"
	"github.com/mattermost/mattermost-plugin-mscalendar/server/store"
	"github.com/mattermost/mattermost-plugin-mscalendar/server/store/mock_store"
	"github.com/mattermost/mattermost-plugin-mscalendar/server/utils/bot"
	"github.com/mattermost/mattermost-plugin-mscalendar/server/utils/bot/mock_bot"
	"github.com/mattermost/mattermost-plugin-mscalendar/server/utils/oauth2connect"
	"github.com/mattermost/mattermost-server/v5/model"
)

const (
	fakeID          = "fake@mattermost.com"
	fakeRemoteID    = "user-remote-id"
	fakeBotID       = "bot-user-id"
	fakeBotRemoteID = "bot-remote-id"
	fakeCode        = "fakecode"
)

func TestCompleteOAuth2Happy(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	statusOKGraphAPIResponder()

	app, env := newOAuth2TestApp(ctrl)
	ss := env.Dependencies.Store.(*mock_store.MockStore)
	poster := env.Dependencies.Poster.(*mock_bot.MockPoster)

	state := ""
	gomock.InOrder(
		ss.EXPECT().LoadUser(fakeID).Return(nil, errors.New("Connected user not found")).Times(1),
		ss.EXPECT().StoreOAuth2State(gomock.Any()).DoAndReturn(
			func(s string) error {
				if !strings.HasSuffix(s, "_"+fakeID) {
					return errors.New("invalid state " + s)
				}
				state = s
				return nil
			},
		).Times(1),
	)

	redirectURL, err := app.InitOAuth2(fakeID)
	require.NoError(t, err)
	require.NotEmpty(t, redirectURL)
	require.NotEmpty(t, state)

	gomock.InOrder(
		ss.EXPECT().VerifyOAuth2State(gomock.Eq(state)).Return(nil).Times(1),
		ss.EXPECT().LoadMattermostUserID(fakeRemoteID).Return("", errors.New("Connected user not found")).Times(1),
		ss.EXPECT().StoreUser(gomock.Any()).Return(nil).Times(1),
		ss.EXPECT().StoreUserInIndex(gomock.Any()).Return(nil).Times(1),
		poster.EXPECT().DM(
			gomock.Eq(fakeID),
			gomock.Eq(WelcomeMessage),
			gomock.Eq("mail-value"),
		).Return(nil).Times(1),
	)

	err = app.CompleteOAuth2(fakeID, fakeCode, state)
	require.NoError(t, err)
}

func TestCompleteOAuth2ForBotHappy(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	statusOKGraphAPIResponder()

	app, env := newOAuth2TestApp(ctrl)
	ss := env.Dependencies.Store.(*mock_store.MockStore)
	poster := env.Dependencies.Poster.(*mock_bot.MockPoster)
	env.Dependencies.IsAuthorizedAdmin = func(mattermostUserID string) (bool, error) { return true, nil }

	state := ""
	gomock.InOrder(
		ss.EXPECT().LoadUser(fakeBotID).Return(nil, errors.New("Connected user not found")).Times(1),
		ss.EXPECT().StoreOAuth2State(gomock.Any()).DoAndReturn(
			func(s string) error {
				if !strings.HasSuffix(s, "_"+fakeBotID) {
					return errors.New("invalid state " + s)
				}
				state = s
				return nil
			},
		).Times(1),
	)

	redirectURL, err := app.InitOAuth2ForBot(fakeID)
	require.NoError(t, err)
	require.NotEmpty(t, redirectURL)
	require.NotEmpty(t, state)

	gomock.InOrder(
		ss.EXPECT().VerifyOAuth2State(gomock.Eq(state)).Return(nil).Times(1),
		ss.EXPECT().LoadMattermostUserID(fakeRemoteID).Return("", errors.New("Connected user not found")).Times(1),
		ss.EXPECT().StoreUser(gomock.Any()).Return(nil).Times(1),
		poster.EXPECT().DM(
			gomock.Eq(fakeID),
			gomock.Eq(BotWelcomeMessage),
			gomock.Eq("mail-value"),
		).Return(nil).Times(1),
	)

	err = app.CompleteOAuth2(fakeID, fakeCode, state)
	require.NoError(t, err)
}

func TestInitOAuth2(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tcs := []struct {
		name             string
		mattermostUserID string
		setup            func(dependencies *Dependencies)
		expectError      bool
		expectURL        string
	}{
		{
			name:             "MM user already connected",
			mattermostUserID: "fake@mattermost.com",
			setup: func(d *Dependencies) {
				su := &store.User{Remote: &remote.User{Mail: "remote_email@example.com"}}
				us := d.Store.(*mock_store.MockStore)
				us.EXPECT().LoadUser("fake@mattermost.com").Return(su, nil).Times(1)
			},
			expectError: true,
		},
		{
			name:             "unable to store user state",
			mattermostUserID: fakeID,
			setup: func(d *Dependencies) {
				ss := d.Store.(*mock_store.MockStore)
				ss.EXPECT().LoadUser(fakeID).Return(nil, errors.New("Remote user not found")).Times(1)
				ss.EXPECT().StoreOAuth2State(gomock.Any()).Return(errors.New("unable to store state")).Times(1)
			},
			expectError: true,
		},
		{
			name:             "successful redirect",
			mattermostUserID: fakeID,
			setup: func(d *Dependencies) {
				ss := d.Store.(*mock_store.MockStore)
				ss.EXPECT().LoadUser(fakeID).Return(nil, errors.New("Remote user not found")).Times(1)
				ss.EXPECT().StoreOAuth2State(gomock.Any()).Return(nil).Times(1)
			},
			expectURL: "https://login.microsoftonline.com/common/oauth2/v2.0/authorize?access_type=offline&client_id=fakeclientid&redirect_uri=http%3A%2F%2Flocalhost%2Foauth2%2Fcomplete&response_type=code&scope=offline_access+User.Read+Calendars.ReadWrite+Calendars.ReadWrite.Shared+Mail.Read+Mail.Send&state=kbb9cs43z3fxxpc_fake%40mattermost.com",
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			app, env := newOAuth2TestApp(ctrl)
			if tc.setup != nil {
				tc.setup(env.Dependencies)
			}
			gotURL, err := app.InitOAuth2(tc.mattermostUserID)
			if tc.expectError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			require.Equal(t, noState(tc.expectURL), noState(gotURL))
		})
	}
}

func TestInitOAuth2ForBot(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tcs := []struct {
		name             string
		mattermostUserID string
		setup            func(dependencies *Dependencies)
		expectError      bool
		expectURL        string
	}{
		{
			name:             "connecting bot, user is not admin",
			mattermostUserID: fakeID,
			setup: func(d *Dependencies) {
				d.IsAuthorizedAdmin = func(userID string) (bool, error) { return false, nil }
			},
			expectError: true,
		},
		{
			name:             "connecting bot, user is admin",
			mattermostUserID: fakeID,
			setup: func(d *Dependencies) {
				d.IsAuthorizedAdmin = func(userID string) (bool, error) {
					return true, nil
				}

				ss := d.Store.(*mock_store.MockStore)
				ss.EXPECT().LoadUser(fakeBotID).Return(nil, errors.New("Remote user not found")).Times(1)
				ss.EXPECT().StoreOAuth2State(gomock.Any()).Return(nil).Times(1)
			},
			expectURL: "https://login.microsoftonline.com/common/oauth2/v2.0/authorize?access_type=offline&client_id=fakeclientid&redirect_uri=http%3A%2F%2Flocalhost%2Foauth2%2Fcomplete&response_type=code&scope=offline_access+User.Read+Calendars.ReadWrite+Calendars.ReadWrite.Shared+Mail.Read+Mail.Send&state=kbb9cs43z3fxxpc_fake_bot-user-id",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			app, env := newOAuth2TestApp(ctrl)
			if tc.setup != nil {
				tc.setup(env.Dependencies)
			}
			gotURL, err := app.InitOAuth2ForBot(tc.mattermostUserID)
			if tc.expectError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			require.Equal(t, noState(tc.expectURL), noState(gotURL))
		})
	}
}

func TestCompleteOAuth2Errors(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	httpmock.Activate()
	defer httpmock.DeactivateAndReset()

	tcs := []struct {
		name              string
		mattermostUserID  string
		code              string
		state             string
		setup             func(*Dependencies)
		registerResponder func()
		expectError       string
	}{
		{
			name:        "missing user",
			expectError: "missing user, code or state",
		},
		{
			name:             "missing authorization code",
			mattermostUserID: fakeID,
			expectError:      "missing user, code or state",
		},
		{
			name:             "missing state",
			mattermostUserID: fakeID,
			code:             fakeCode,
			expectError:      "missing user, code or state",
		},
		{
			name:             "user state not authorized",
			mattermostUserID: fakeID,
			code:             fakeCode,
			state:            "user_nomatch@mattermost.com",
			setup: func(d *Dependencies) {
				ss := d.Store.(*mock_store.MockStore)
				ss.EXPECT().VerifyOAuth2State(gomock.Eq("user_nomatch@mattermost.com")).Return(nil).Times(1)
			},
			expectError: "not authorized, user ID mismatch",
		},
		{
			name:             "connecting bot user, not admin",
			mattermostUserID: "bot_user_id",
			code:             fakeCode,
			state:            "fake_bot-user-id",
			setup: func(d *Dependencies) {
				d.IsAuthorizedAdmin = func(mattermostUserID string) (bool, error) {
					return false, nil
				}

				ss := d.Store.(*mock_store.MockStore)
				ss.EXPECT().VerifyOAuth2State(gomock.Eq("fake_bot-user-id")).Return(nil).Times(1)
			},
		},
		{
			name:              "unable to exchange auth code for token",
			mattermostUserID:  fakeID,
			code:              fakeCode,
			state:             "user_" + fakeID,
			registerResponder: badTokenExchangeResponder,
			setup: func(d *Dependencies) {
				ss := d.Store.(*mock_store.MockStore)
				ss.EXPECT().VerifyOAuth2State(gomock.Eq("user_fake@mattermost.com")).Return(nil).Times(1)
			},
			expectError: "cannot fetch token: 400",
		},
		{
			name:              "Remote user already connected",
			mattermostUserID:  "fake@mattermost.com",
			code:              fakeCode,
			state:             "user_fake@mattermost.com",
			registerResponder: statusOKGraphAPIResponder,
			setup: func(d *Dependencies) {
				papi := d.PluginAPI.(*mock_plugin_api.MockPluginAPI)
				papi.EXPECT().GetMattermostUser("fake@mattermost.com").Return(&model.User{Username: "sample-username"}, nil)

				ss := d.Store.(*mock_store.MockStore)
				ss.EXPECT().LoadMattermostUserID("user-remote-id").Return("fake@mattermost.com", nil)
				ss.EXPECT().VerifyOAuth2State(gomock.Eq("user_fake@mattermost.com")).Return(nil).Times(1)

				poster := d.Poster.(*mock_bot.MockPoster)
				poster.EXPECT().DM(
					gomock.Eq("fake@mattermost.com"),
					gomock.Eq(RemoteUserAlreadyConnected),
					gomock.Eq("Microsoft Calendar"),
					gomock.Eq("mail-value"),
					gomock.Eq("mscalendar"),
					gomock.Eq("sample-username"),
				).Return(nil).Times(1)
			},
		},
		{
			name:              "microsoft graph mscalendar client unable to get user info",
			mattermostUserID:  fakeID,
			code:              fakeCode,
			state:             "user_fake@mattermost.com",
			registerResponder: unauthorizedTokenGraphAPIResponder,
			setup: func(d *Dependencies) {
				ss := d.Store.(*mock_store.MockStore)
				ss.EXPECT().VerifyOAuth2State(gomock.Eq("user_fake@mattermost.com")).Return(nil).Times(1)
			},
			expectError: "Access token is empty",
		},
		{
			name:              "UserStore unable to store user info",
			mattermostUserID:  fakeID,
			code:              fakeCode,
			state:             "user_fake@mattermost.com",
			registerResponder: statusOKGraphAPIResponder,
			setup: func(d *Dependencies) {
				ss := d.Store.(*mock_store.MockStore)
				ss.EXPECT().StoreUser(gomock.Any()).Return(errors.New("forced kvstore error")).Times(1)
				ss.EXPECT().LoadMattermostUserID("user-remote-id").Return("", errors.New("Connected user not found")).Times(1)
				ss.EXPECT().VerifyOAuth2State(gomock.Eq("user_fake@mattermost.com")).Return(nil).Times(1)
			},
			expectError: "forced kvstore error",
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			if tc.registerResponder != nil {
				tc.registerResponder()
			}

			app, env := newOAuth2TestApp(ctrl)
			if tc.setup != nil {
				tc.setup(env.Dependencies)
			}

			err := app.CompleteOAuth2(tc.mattermostUserID, tc.code, tc.state)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.expectError)
		})
	}
}

func badTokenExchangeResponder() {
	url := "https://login.microsoftonline.com/common/oauth2/v2.0/token"

	responder := httpmock.NewStringResponder(http.StatusBadRequest, `{"error":"invalid request"}`)

	httpmock.RegisterResponder("POST", url, responder)
}

func unauthorizedTokenGraphAPIResponder() {
	tokenURL := "https://login.microsoftonline.com/common/oauth2/v2.0/token"

	tokenResponse := `{
    "token_type": "Bearer",
    "scope": "user.read%20Fmail.read",
    "expires_in": 3600,
    "access_token": "eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiIsIng1dCI6Ik5HVEZ2ZEstZnl0aEV1Q...",
    "refresh_token": "AwABAAAAvPM1KaPlrEqdFSBzjqfTGAMxZGUTdM0t4B4..."
}`

	tokenResponder := httpmock.NewStringResponder(http.StatusOK, tokenResponse)

	httpmock.RegisterResponder("POST", tokenURL, tokenResponder)

	meRequestURL := "https://graph.microsoft.com/v1.0/me"

	meResponse := `{
    "error": {
        "code": "InvalidAuthenticationToken",
        "message": "Access token is empty.",
        "innerError": {
            "request-id": "d1a6e016-c7c4-4caf-9a7f-2d7079dc05d2",
            "date": "2019-11-12T00:49:46"
        }
    }
}`

	meResponder := httpmock.NewStringResponder(http.StatusUnauthorized, meResponse)

	httpmock.RegisterResponder("GET", meRequestURL, meResponder)
}

func statusOKGraphAPIResponder() {
	tokenURL := "https://login.microsoftonline.com/common/oauth2/v2.0/token"

	tokenResponse := `{
    "token_type": "Bearer",
    "scope": "user.read%20Fmail.read",
    "expires_in": 3600,
    "access_token": "eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiIsIng1dCI6Ik5HVEZ2ZEstZnl0aEV1Q...",
    "refresh_token": "AwABAAAAvPM1KaPlrEqdFSBzjqfTGAMxZGUTdM0t4B4..."
}`

	tokenResponder := httpmock.NewStringResponder(http.StatusOK, tokenResponse)

	httpmock.RegisterResponder("POST", tokenURL, tokenResponder)

	meRequestURL := "https://graph.microsoft.com/v1.0/me"

	meResponse := `{
    "businessPhones": [
        "businessPhones-value"
    ],
    "displayName": "displayName-value",
    "givenName": "givenName-value",
    "jobTitle": "jobTitle-value",
    "mail": "mail-value",
    "mobilePhone": "mobilePhone-value",
    "officeLocation": "officeLocation-value",
    "preferredLanguage": "preferredLanguage-value",
    "surname": "surname-value",
    "userPrincipalName": "userPrincipalName-value",
    "id": "user-remote-id"
}`

	meResponder := httpmock.NewStringResponder(http.StatusOK, meResponse)

	httpmock.RegisterResponder("GET", meRequestURL, meResponder)
}

func newHTTPRequest(mattermostUserID, rawQuery string) *http.Request {
	r := &http.Request{
		Header: make(http.Header),
		URL: &url.URL{
			RawQuery: rawQuery,
		},
	}
	r.Header.Add("Mattermost-User-ID", mattermostUserID)
	return r
}

func newOAuth2TestApp(ctrl *gomock.Controller) (oauth2connect.App, Env) {
	conf := &config.Config{
		StoredConfig: config.StoredConfig{
			OAuth2Authority:    "common",
			OAuth2ClientID:     "fakeclientid",
			OAuth2ClientSecret: "fakeclientsecret",
		},
		PluginURL: "http://localhost",
		BotUserID: "bot-user-id",
	}

	env := Env{
		Config: conf,
		Dependencies: &Dependencies{
			Store:             mock_store.NewMockStore(ctrl),
			Logger:            &bot.NilLogger{},
			Poster:            mock_bot.NewMockPoster(ctrl),
			Remote:            remote.Makers[msgraph.Kind](conf, &bot.NilLogger{}),
			PluginAPI:         mock_plugin_api.NewMockPluginAPI(ctrl),
			IsAuthorizedAdmin: func(mattermostUserID string) (bool, error) { return false, nil },
		},
	}

	return NewOAuth2App(env), env
}

var stateRegexp = regexp.MustCompile(`^(?P<before>.*)&+state=\w+(?P<after>.*)$`)

func noState(in string) string {
	return stateRegexp.ReplaceAllString(in, "$before$after")
}
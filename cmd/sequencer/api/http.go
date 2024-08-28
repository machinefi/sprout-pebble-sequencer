package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/machinefi/ioconnect-go/pkg/ioconnect"
	"github.com/pkg/errors"

	"github.com/machinefi/sprout-pebble-sequencer/cmd/sequencer/apitypes"
	"github.com/machinefi/sprout-pebble-sequencer/cmd/sequencer/clients"
	"github.com/machinefi/sprout-pebble-sequencer/pkg/models"
	"github.com/machinefi/sprout-pebble-sequencer/pkg/modules/event"
)

type httpServer struct {
	ctx     context.Context
	engine  *gin.Engine
	jwk     *ioconnect.JWK
	clients *clients.Manager
}

func NewHttpServer(ctx context.Context, jwk *ioconnect.JWK, clientMgr *clients.Manager) *httpServer {
	s := &httpServer{
		ctx:     ctx,
		engine:  gin.Default(),
		jwk:     jwk,
		clients: clientMgr,
	}

	slog.Debug("jwk information",
		"did:io", jwk.DID(),
		"did:io#key", jwk.KID(),
		"ka did:io", jwk.KeyAgreementDID(),
		"ka did:io#key", jwk.KeyAgreementKID(),
		"doc", jwk.Doc(),
	)

	s.engine.POST("/issue_vc", s.issueJWTCredential)
	s.engine.POST("/device/:imei/confirm", s.verifyToken, s.confirmDevice)
	s.engine.GET("/device/:imei/query", s.verifyToken, s.queryDeviceState)
	s.engine.GET("/didDoc", s.didDoc)

	return s
}

// this func will block caller
func (s *httpServer) Run(address string) error {
	if err := s.engine.Run(address); err != nil {
		return errors.Wrap(err, "failed to start http server")
	}
	return nil
}

// verifyToken make sure the client token is issued by sequencer
func (s *httpServer) verifyToken(c *gin.Context) {
	tok := c.GetHeader("Authorization")
	if tok == "" {
		tok = c.Query("authorization")
	}

	if tok == "" {
		return
	}

	tok = strings.TrimSpace(strings.Replace(tok, "Bearer", " ", 1))

	clientID, err := s.jwk.VerifyToken(tok)
	if err != nil {
		c.JSON(http.StatusUnauthorized, apitypes.NewErrRsp(errors.Wrap(err, "invalid credential token")))
		return
	}
	client := s.clients.ClientByIoID(clientID)
	if client == nil {
		c.JSON(http.StatusUnauthorized, apitypes.NewErrRsp(errors.New("invalid credential token")))
		return
	}

	ctx := clients.WithClientID(c.Request.Context(), client)
	c.Request = c.Request.WithContext(ctx)
}

func (s *httpServer) confirmDevice(c *gin.Context) {
	//imei := c.Param("imei")

}

func (s *httpServer) queryDeviceState(c *gin.Context) {
	imei := c.Param("imei")

	dev := &models.Device{ID: imei}
	if err := event.FetchByPrimary(s.ctx, dev); err != nil {
		c.JSON(http.StatusInternalServerError, apitypes.NewErrRsp(err))
		return
	}
	if dev.Status == models.CREATED {
		c.JSON(http.StatusBadRequest, apitypes.NewErrRsp(errors.Errorf("device %s is not propsaled", dev.ID)))
		return
	}
	var (
		firmware string
		uri      string
		version  string
	)
	if parts := strings.Split(dev.RealFirmware, " "); len(parts) == 2 {
		app := &models.App{ID: parts[0]}
		err := event.FetchByPrimary(s.ctx, app)
		if err == nil {
			firmware = app.ID
			uri = app.Uri
			version = app.Version
		}
	}

	// meta := contexts.AppMeta().MustFrom(ctx)
	//pubType := "pub_DeviceQueryRsp"
	pubData := &struct {
		Status     int32  `json:"status"`
		Proposer   string `json:"proposer"`
		Firmware   string `json:"firmware,omitempty"`
		URI        string `json:"uri,omitempty"`
		Version    string `json:"version,omitempty"`
		ServerMeta string `json:"server_meta,omitempty"`
	}{
		Status:   dev.Status,
		Proposer: dev.Proposer,
		Firmware: firmware,
		URI:      uri,
		Version:  version,
		// ServerMeta: meta.String(),
	}

	// if client != nil {
	// 	slog.Info("encrypt response task query", "response", response)
	// 	cipher, err := s.jwk.EncryptJSON(response, client.KeyAgreementKID())
	// 	if err != nil {
	// 		c.JSON(http.StatusInternalServerError, apitypes.NewErrRsp(errors.Wrap(err, "failed to encrypt response when query task")))
	// 		return
	// 	}
	// 	c.Data(http.StatusOK, "application/octet-stream", cipher)
	// 	return
	// }

	c.JSON(http.StatusOK, pubData)
}

func (s *httpServer) didDoc(c *gin.Context) {
	if s.jwk == nil {
		c.JSON(http.StatusNotAcceptable, apitypes.NewErrRsp(errors.New("jwk is not config")))
		return
	}
	c.JSON(http.StatusOK, s.jwk.Doc())
}

func (s *httpServer) issueJWTCredential(c *gin.Context) {
	req := new(apitypes.IssueTokenReq)
	if err := c.ShouldBindJSON(req); err != nil {
		c.JSON(http.StatusBadRequest, apitypes.NewErrRsp(err))
		return
	}

	client := s.clients.ClientByIoID(req.ClientID)
	if client == nil {
		c.String(http.StatusForbidden, errors.Errorf("client is not register to ioRegistry").Error())
		return
	}

	token, err := s.jwk.SignToken(req.ClientID)
	if err != nil {
		c.String(http.StatusInternalServerError, errors.Wrap(err, "failed to sign token").Error())
		return
	}
	slog.Info("token signed", "token", token)

	cipher, err := s.jwk.Encrypt([]byte(token), client.KeyAgreementKID())
	if err != nil {
		c.String(http.StatusInternalServerError, errors.Wrap(err, "failed to encrypt").Error())
		return
	}

	c.Data(http.StatusOK, "application/json", cipher)
}

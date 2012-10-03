package entry

import (
	"appengine"
	"appengine/user"
	"appengine/xmpp"
	"bytes"
	"encoding/base64"
	"event"
	"fmt"
	"handler"
	"misc"
	"net"
	"net/http"
	"service"
	"strconv"
	"strings"
)

var serverInited bool = false

func init() {
	event.InitEvents(new(handler.DispatchEventHandler))
	http.HandleFunc("/", IndexEntry)
	http.HandleFunc("/admin", AdminEntry)
	//http.HandleFunc("/dns", dnsHandler)
	http.HandleFunc("/invoke", HTTPEventDispatch)
	http.HandleFunc("/_ah/start", BackendInit)
	//warmup request is no available in GO runtime now
	http.HandleFunc("/_ah/warmup", InitGAEServer)
	xmpp.Handle(XMPPEventDispatch)
}

func dnsHandler(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Query().Get("Host")
	if len(host) > 0 {
		if addrs, err := net.LookupHost(host); nil == err {
			w.Write([]byte(fmt.Sprintf("[%s]", strings.Join(addrs, ","))))
			return
		} else {
			w.Write([]byte(fmt.Sprintf("%v", err)))
		}
	}
	w.Write([]byte("No Host para."))
}

func initGAEProxyServer(ctx appengine.Context) {
	if !serverInited {
		service.LoadServerConfig(ctx)
		service.CheckDefaultAccount(ctx)
		if service.ServerConfig.IsMaster == 1 {
			service.InitMasterService(ctx)
		}
		ctx.Infof("InitGAEServer Invoked!")
		serverInited = true
	}
}

func InitGAEServer(w http.ResponseWriter, r *http.Request) {
	ctx := appengine.NewContext(r)
	initGAEProxyServer(ctx)
	w.WriteHeader(http.StatusOK)
}

const adminFrom = `
<html>
  <head>
    <meta http-equiv="content-type" content="text/html; charset=UTF-8">
    <title>snova-gae(Go) V%s admin</title>
  </head>
  <body>
    <table width="800" border="0" align="center">
            <tr><td align="center">
                <b><h1>root password:%s</h1></b>
            </td></tr>
             <tr><td align="center">
                <a href="%s">sign out</a>
            </td></tr>
    </table>
  </body>
</html>
`

const signoutFrom = `
<html>
  <head>
    <meta http-equiv="content-type" content="text/html; charset=UTF-8">
    <title>snova-gae(Go) %s admin</title>
  </head>
  
   <body>
    <table width="800" border="0" align="center">
            <tr><td align="center">
                <p>Hello, %s! You are not the admin of this application, please 
<a href="%s">sign out</a> first, then login again.</p>
            </td></tr>
    </table>
    
  </body>
</html>
`

func AdminEntry(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	u := user.Current(c)
	if u == nil {
		url, err := user.LoginURL(c, r.URL.String())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Location", url)
		w.WriteHeader(http.StatusFound)
		return
	}
	if !user.IsAdmin(c) {
		url, _ := user.LogoutURL(c, "/admin")
		fmt.Fprintf(w, signoutFrom, misc.Version, u.String(), url)
		return
	}
	url, _ := user.LogoutURL(c, "/")
	root := service.GetUserWithName(c, "root")
	fmt.Fprintf(w, adminFrom, misc.Version, root.Passwd, url)
}

const indexForm = `
<html>
  <head>
    <meta http-equiv="content-type" content="text/html; charset=UTF-8">
    <title>snova-gae(Go) %s</title>
  </head>

  <body>
    <table width="800" border="0" align="center">
            <tr><td align="center">
                <b><h1>snova-gae(Go) %s server is running!</h1></b>
            </td></tr>
            <tr><td align="center">
                <a href="/admin">admin</a>
            </td></tr>
    </table>
  </body>
</html>
`

func IndexEntry(w http.ResponseWriter, r *http.Request) {
	//ctx := appengine.NewContext(r)
	fmt.Fprintf(w, indexForm, misc.Version, misc.Version)
}

func BackendInit(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

type HTTPEventSendService struct {
	writer http.ResponseWriter
}

func (serv *HTTPEventSendService) GetMaxDataPackageSize() int {
	return -1
}

func (serv *HTTPEventSendService) Send(buf *bytes.Buffer) {
	headers := serv.writer.Header()
	headers.Add("Content-Type", "application/octet-stream")
	headers.Add("Content-Length", strconv.Itoa(buf.Len()))
	serv.writer.WriteHeader(http.StatusOK)
	serv.writer.Write(buf.Bytes())
}

type XMPPEventSendService struct {
	jid  string
	from string
	ctx  appengine.Context
}

func (serv *XMPPEventSendService) GetMaxDataPackageSize() int {
	return int(service.ServerConfig.MaxXMPPDataPackageSize)
}
func (serv *XMPPEventSendService) Send(buf *bytes.Buffer) {
	body := base64.StdEncoding.EncodeToString(buf.Bytes())
	var msg xmpp.Message
	msg.Body = body
	msg.Sender = serv.jid
	msg.To = []string{serv.from}
	msg.Type = "chat"
	retryCount := service.ServerConfig.RetryFetchCount
	for retryCount > 0 {
		err := msg.Send(serv.ctx)
		if nil == err {
			return
		}
		retryCount--
		serv.ctx.Errorf("Failed to send xmpp(%d:%d bytes) for reason:%s", len(body), buf.Len(), err.Error())
	}
}

func XMPPEventDispatch(ctx appengine.Context, m *xmpp.Message) {
	initGAEProxyServer(ctx)
	src, err := base64.StdEncoding.DecodeString(m.Body)
	if nil != err {
		ctx.Errorf("Failed to decode base64 XMPP.")
		return
	}
	success, ev, tags, cause := event.ParseEventWithTags(src)
	if success {
		serv := new(XMPPEventSendService)
		serv.jid = m.To[0]
		serv.from = m.Sender
		serv.ctx = ctx
		attach := make([]interface{}, 3)
		attach[0] = tags
		attach[1] = ctx
		attach[2] = serv
		ev.SetAttachement(attach)
		event.DiaptchEvent(ev)
		return
	}
	ctx.Errorf("Failed to parse XMPP event:" + cause)
}

func HTTPEventDispatch(w http.ResponseWriter, r *http.Request) {
	ctx := appengine.NewContext(r)
	initGAEProxyServer(ctx)
	buf := make([]byte, r.ContentLength)
	r.Body.Read(buf)
	serv := new(HTTPEventSendService)
	serv.writer = w
	success, ev, tags, cause := event.ParseEventWithTags(buf)
	if success {
		attach := make([]interface{}, 3)
		attach[0] = tags
		attach[1] = ctx
		attach[2] = serv
		ev.SetAttachement(attach)
		event.DiaptchEvent(ev)
		return
	}
	ctx.Errorf("Failed to parse HTTP event:" + cause)
	fmt.Fprintf(w, "Failed to parse HTTP event:"+cause)
}

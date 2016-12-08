package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/markdaws/gohome"
	"github.com/markdaws/gohome/log"
	"github.com/markdaws/gohome/validation"
	"github.com/urfave/negroni"
)

type apiServer struct {
	systemSavePath string
	system         *gohome.System
	sessions       *gohome.Sessions
}

// ListenAndServe creates a new WWW server, that handles API calls and also
// runs the gohome website
func ListenAndServe(
	systemSavePath string,
	addr string,
	system *gohome.System,
	sessions *gohome.Sessions) error {
	server := &apiServer{
		systemSavePath: systemSavePath,
		system:         system,
		sessions:       sessions,
	}
	return server.listenAndServe(addr)
}

func (s *apiServer) listenAndServe(addr string) error {

	r := mux.NewRouter()

	RegisterSceneHandlers(r, s)
	RegisterDeviceHandlers(r, s)
	RegisterDiscoveryHandlers(r, s)
	RegisterMonitorHandlers(r, s)
	RegisterUserHandlers(r, s)
	RegisterAutomationHandlers(r, s)

	n := negroni.New()
	n.Use(negroni.HandlerFunc(CheckValidSession(s.sessions)))
	n.UseHandler(r)

	server := &http.Server{
		Addr:         addr,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		Handler: handlers.CORS(
			handlers.AllowedMethods([]string{"PUT", "POST", "DELETE", "GET", "OPTIONS", "UPGRADE"}),
			handlers.AllowedOrigins([]string{"*"}),
			handlers.AllowedHeaders([]string{"content-type"}),
		)(n),
	}
	return server.ListenAndServe()
}

func CheckValidSession(sessions *gohome.Sessions) func(rw http.ResponseWriter, r *http.Request, next http.HandlerFunc) {

	return func(rw http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
		pairs, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			rw.WriteHeader(http.StatusUnauthorized)
			return
		}

		sid, ok := pairs["sid"]
		if !ok || len(sid) == 0 {
			rw.WriteHeader(http.StatusUnauthorized)
			return
		}

		_, ok = sessions.Get(sid[0])
		if !ok {
			rw.WriteHeader(http.StatusUnauthorized)
			return
		}

		// If we got here, the user has a valid session ID, go to next handler
		next(rw, r)
	}
}

// apiResponse encapsulates the response from a http handler, responses can either
// be an error, such as invalid input, or contains a sucessful response
type apiResponse struct {
	// Err - will be non nil if there was a error processing the API request
	Err error

	// Data - pointer to struct that can be serialized to JSON that will then
	// be sent back to the client
	Data interface{}
}

// badRequestErr - API input was incorrect, e.g. missing required field.  The Msg field
// contains more specific information about the error
type badRequestErr struct {
	Msg string
}

func (r *badRequestErr) Error() string {
	return r.Msg
}

// validationErr - an error that occurs when input fields are not valid e.g. Name field
// is too long etc.
type validationErr struct {
	ID     string
	Data   interface{}
	Errors *validation.Errors
}

func (e *validationErr) Error() string {
	return e.Errors.Error()
}

// respBadRequest responds to the client with a http.StatusBadRequest and additional message
func respBadRequest(msg string, w http.ResponseWriter) {
	resp(apiResponse{
		Err: &badRequestErr{
			Msg: msg,
		},
	}, w)
}

func respValErr(data interface{}, ID string, errs *validation.Errors, w http.ResponseWriter) {
	resp(apiResponse{
		Err: &validationErr{
			ID:     ID,
			Data:   data,
			Errors: errs,
		},
	}, w)
}

func respErr(err error, w http.ResponseWriter) {
	resp(apiResponse{Err: err}, w)
}

func resp(r apiResponse, w http.ResponseWriter) {
	if r.Err != nil {
		switch err := r.Err.(type) {
		case *validationErr:
			w.WriteHeader(http.StatusBadRequest)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			json.NewEncoder(w).Encode(struct {
				Err struct {
					ValErr validation.ErrorJSON `json:"validation"`
				} `json:"err"`
			}{Err: struct {
				ValErr validation.ErrorJSON `json:"validation"`
			}{validation.NewErrorJSON(err.Data, err.ID, err.Errors)}})
		case *badRequestErr:
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(struct {
				Err struct {
					Msg string `json:"msg"`
				} `json:"err"`
			}{Err: struct {
				Msg string `json:"msg"`
			}{err.Msg}})
		default:
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(struct {
				Err struct {
					Msg string `json:"msg"`
				} `json:"err"`
			}{Err: struct {
				Msg string `json:"msg"`
			}{err.Error()}})
		}
		return
	}

	if r.Data != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		err := json.NewEncoder(w).Encode(r.Data)
		if err != nil {
			log.V("error writing JSON to client %s", err)
		}
	}
}

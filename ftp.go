// Package ftp implements a FTP client as described in RFC 959.
package ftp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	// Time Layout used by MLSD/MLST (without fractions of second)
	TimeLayoutMlsx = "20060102150405"
	// Time Layout used by MLSD/MLST (including fractions of second)
	TimeLayoutMlsxFrac = "20060102150405.9"
)

// EntryType describes the different types of an Entry.
type EntryType int

const (
	EntryTypeFile   EntryType = iota // file
	EntryTypeFolder                  // directory
	EntryTypeLink                    // symlink
)

// ServerConn represents the connection to a remote FTP server.
type ServerConn struct {
	conn     *textproto.Conn
	host     string
	features map[string]string

	// translate filename encoding from/to ISO 8859-15 if server does not support UTF-8
	TranslateEncoding bool
	// list "." and ".."
	ListDotDirs bool
}

// Entry describes a file and is returned by List().
type Entry struct {
	Name string
	Type EntryType
	Size uint64
	Time time.Time
}

// EntryEx describes a file and is returned by MList() and MInfo().
// EntryEx implements the FileInfo interface
type EntryEx struct {
	// name of the file
	name string
	// facts describing the file. Keys for standard facts (lowercase): size, modify, create, type (), unique, perm, lang, media-type, charset
	Facts map[string]string
}

// Name returns the name of the FTP directory entry
func (e EntryEx) Name() string {
	return e.name
}

// SetName sets the name of the FTP directory entry
func (e *EntryEx) SetName(s string) {
	e.name = s
}

// Size returns the size
func (e EntryEx) Size() int64 {
	sSize, exists := e.Facts["size"]
	if !exists {
		return 0
	}
	size, err := strconv.Atoi(sSize)
	if err != nil {
		return 0
	}
	return int64(size)
}

// Mode returns the file permissions and other flags
func (e EntryEx) Mode() os.FileMode {
	var mode os.FileMode
	sPerm, pExists := e.Facts["perm"]
	//sType, tExists := e.Facts["type"]
	if pExists {
		if strings.Contains(sPerm, "r") {
			mode += 0400
		}
		if strings.Contains(sPerm, "w") {
			mode += 0200
		}
	}
	if e.IsDir() {
		mode += os.ModeDir
	}
	return mode
}

// ModTime returns the last modified time
func (e EntryEx) ModTime() time.Time {
	sModify, exists := e.Facts["modify"]
	if !exists {
		return time.Unix(0, 0)
	}
	modify, err := ParseMListTime(sModify)
	if err != nil {
		return time.Unix(0, 0)
	}
	return modify
}

// IsDir
func (e EntryEx) IsDir() bool {
	eType, exists := e.Facts["type"]
	if !exists {
		return false
	}
	return (eType == "dir") || (eType == "cdir") || (eType == "pdir")
}

// Sys returns the underlying data source (can and does return nil)
func (e EntryEx) Sys() interface{} {
	return nil
}

// response represent a data-connection
type response struct {
	conn net.Conn
	c    *ServerConn
}

// Connect initializes the connection to the specified ftp server address.
//
// It is generally followed by a call to Login() as most FTP commands require
// an authenticated user.
func Connect(addr string) (*ServerConn, error) {
	conn, err := textproto.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		conn.Close()
		return nil, err
	}
	c := &ServerConn{
		conn:     conn,
		host:     host,
		features: make(map[string]string),
	}

	_, _, err = c.conn.ReadResponse(StatusReady)
	if err != nil {
		c.Quit()
		return nil, err
	}

	err = c.feat()
	if err != nil {
		c.Quit()
		return nil, err
	}

	return c, nil
}

// Login authenticates the client with specified user and password.
//
// "anonymous"/"anonymous" is a common user/password scheme for FTP servers
// that allows anonymous read-only accounts.
func (c *ServerConn) Login(user, password string) error {
	code, message, err := c.cmd(-1, "USER %s", user)
	if err != nil {
		return err
	}

	switch code {
	case StatusLoggedIn:
	case StatusUserOK:
		_, _, err = c.cmd(StatusLoggedIn, "PASS %s", password)
		if err != nil {
			return err
		}
	default:
		return errors.New(message)
	}

	// Switch to binary mode
	_, _, err = c.cmd(StatusCommandOK, "TYPE I")
	if err != nil {
		return err
	}

	return nil
}

// feat issues a FEAT FTP command to list the additional commands supported by
// the remote FTP server.
// FEAT is described in RFC 2389
func (c *ServerConn) feat() error {
	code, message, err := c.cmd(-1, "FEAT")
	if err != nil {
		return err
	}

	if code != StatusSystem {
		// The server does not support the FEAT command. This is not an
		// error: we consider that there is no additional feature.
		return nil
	}

	lines := strings.Split(message, "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, " ") {
			continue
		}

		line = strings.TrimSpace(line)
		featureElements := strings.SplitN(line, " ", 2)

		command := featureElements[0]

		var commandDesc string
		if len(featureElements) == 2 {
			commandDesc = featureElements[1]
		}

		c.features[command] = commandDesc
	}

	return nil
}

// converts a string from UTF-8 to the encoding used by the server
// (if the server doesn't support UTF-8, ISO8859-15 is assumed)
func (c *ServerConn) toServerEncoding(s string) string {
	_, utf8Supported := c.features["UTF8"]
	if !utf8Supported && c.TranslateEncoding {
		s = UTF8ToISO8859_15(s)
	}
	return s
}

// converts a string from the encoding used by the server to UTF-8
// (if the server doesn't support UTF-8, ISO8859-15 is assumed)
func (c *ServerConn) fromServerEncoding(s string) string {
	_, utf8Supported := c.features["UTF8"]
	if !utf8Supported && c.TranslateEncoding {
		s = ISO8859_15ToUTF8(s)
	}
	return s
}

// epsv issues an "EPSV" command to get a port number for a data connection.
func (c *ServerConn) epsv() (port int, err error) {
	_, line, err := c.cmd(StatusExtendedPassiveMode, "EPSV")
	if err != nil {
		return
	}

	start := strings.Index(line, "|||")
	end := strings.LastIndex(line, "|")
	if start == -1 || end == -1 {
		err = errors.New("invalid EPSV response format")
		return
	}
	port, err = strconv.Atoi(line[start+3 : end])
	return
}

// pasv issues a "PASV" command to get a port number for a data connection.
func (c *ServerConn) pasv() (port int, err error) {
	_, line, err := c.cmd(StatusPassiveMode, "PASV")
	if err != nil {
		return
	}

	// PASV response format : 227 Entering Passive Mode (h1,h2,h3,h4,p1,p2).
	start := strings.Index(line, "(")
	end := strings.LastIndex(line, ")")
	if start == -1 || end == -1 {
		err = errors.New("invalid EPSV response format")
		return
	}

	// We have to split the response string
	pasvData := strings.Split(line[start+1:end], ",")
	// Let's compute the port number
	portPart1, err1 := strconv.Atoi(pasvData[4])
	if err1 != nil {
		err = err1
		return
	}

	portPart2, err2 := strconv.Atoi(pasvData[5])
	if err2 != nil {
		err = err2
		return
	}

	// Recompose port
	port = portPart1*256 + portPart2
	return
}

// openDataConn creates a new FTP data connection.
func (c *ServerConn) openDataConn() (net.Conn, error) {
	var port int
	var err error

	//  If features contains nat6 or EPSV => EPSV
	//  else -> PASV
	_, nat6Supported := c.features["nat6"]
	_, epsvSupported := c.features["EPSV"]

	if !nat6Supported && !epsvSupported {
		port, _ = c.pasv()
	}
	if port == 0 {
		port, err = c.epsv()
		if err != nil {
			return nil, err
		}
	}

	// Build the new net address string
	addr := net.JoinHostPort(c.host, strconv.Itoa(port))

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

// cmd is a helper function to execute a command and check for the expected FTP
// return code
func (c *ServerConn) cmd(expected int, format string, args ...interface{}) (int, string, error) {
	_, err := c.conn.Cmd(format, args...)
	if err != nil {
		return 0, "", err
	}

	code, line, err := c.conn.ReadResponse(expected)
	return code, line, err
}

// cmdDataConnFrom executes a command which requires a FTP data connection.
// Issues a REST FTP command to specify the number of bytes to skip for the transfer.
func (c *ServerConn) cmdDataConnFrom(offset uint64, format string, args ...interface{}) (net.Conn, error) {
	conn, err := c.openDataConn()
	if err != nil {
		return nil, err
	}

	if offset != 0 {
		_, _, err := c.cmd(StatusRequestFilePending, "REST %d", offset)
		if err != nil {
			return nil, err
		}
	}

	_, err = c.conn.Cmd(format, args...)
	if err != nil {
		conn.Close()
		return nil, err
	}

	code, msg, err := c.conn.ReadCodeLine(-1)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if code != StatusAlreadyOpen && code != StatusAboutToSend {
		conn.Close()
		return nil, &textproto.Error{Code: code, Msg: msg}
	}

	return conn, nil
}

// parseListLine parses the various non-standard format returned by the LIST
// FTP command.
func (c *ServerConn) parseListLine(line string) (*Entry, error) {
	fields := strings.Fields(line)
	if len(fields) < 9 {
		return nil, errors.New("unsupported LIST line")
	}

	// fields:
	// 0 - type
	// 4 - size
	// 5 - month
	// 6 - day
	// 7 - year|hour:min

	e := &Entry{}
	switch fields[0][0] {
	case '-':
		e.Type = EntryTypeFile
	case 'd':
		e.Type = EntryTypeFolder
	case 'l':
		e.Type = EntryTypeLink
	default:
		return nil, errors.New("unknown entry type")
	}

	if e.Type == EntryTypeFile {
		size, err := strconv.ParseUint(fields[4], 10, 0)
		if err != nil {
			return nil, err
		}
		e.Size = size
	}
	var timeStr string
	setYear, currMon, _ := time.Now().Date()
	ts, err := time.Parse("02 Jan 06", "01 "+fields[5]+" 01")
	if err != nil {
		return nil, err
	}
	dateMon := ts.Month()
	if dateMon > currMon {
		setYear--
	}
	if strings.Contains(fields[7], ":") {
		// year hidden (may be this or prev. year), time present
		timeStr = fields[6] + " " + fields[5] + " " + strconv.Itoa(setYear)[2:4] + " " + fields[7]
	} else {
		// year present, time hidden
		timeStr = fields[6] + " " + fields[5] + " " + fields[7][2:4] + " " + "00:00"
	}
	loc, _ := time.LoadLocation("Local")
	t, err := time.ParseInLocation("_2 Jan 06 15:04", timeStr, loc)
	if err != nil {
		return nil, err
	}
	e.Time = t.Local()

	e.Name = c.fromServerEncoding(strings.Join(fields[8:], " "))
	return e, nil
}

// NameList issues an NLST FTP command.
func (c *ServerConn) NameList(path string) (entries []string, err error) {
	path = c.toServerEncoding(path)
	conn, err := c.cmdDataConnFrom(0, "NLST %s", path)
	if err != nil {
		return
	}

	r := &response{conn, c}
	defer r.Close()

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		entries = append(entries, c.fromServerEncoding(scanner.Text()))
	}
	if err = scanner.Err(); err != nil {
		return entries, err
	}
	return
}

// List issues a LIST FTP command.
func (c *ServerConn) List(path string) (entries []*Entry, err error) {
	path = c.toServerEncoding(path)
	conn, err := c.cmdDataConnFrom(0, "LIST %s", path)
	if err != nil {
		return
	}

	r := &response{conn, c}
	defer r.Close()

	bio := bufio.NewReader(r)
	for {
		line, e := bio.ReadString('\n')
		if e == io.EOF {
			break
		} else if e != nil {
			return nil, e
		}
		entry, err := c.parseListLine(line)
		if err == nil {
			entries = append(entries, entry)
		}
	}
	return
}

// ParseMListTime parses a time fact returned by MLS(D|T). Format is YYYYMMDDHHMMSS[.F...]
func ParseMListTime(sTime string) (t time.Time, err error) {
	timeLayout := TimeLayoutMlsx
	if strings.Contains(sTime, ".") {
		timeLayout = TimeLayoutMlsxFrac
	}
	t, err = time.Parse(timeLayout, sTime)
	return
}

// parseMListLine parses the (hopefully) standard format returned by the MLS(D|T) FTP command.
func (c *ServerConn) parseMListLine(line string) (e EntryEx, err error) {
	line = strings.Trim(line, " \r\n\t")
	fields := strings.Split(line, ";")

	e.Facts = make(map[string]string)
	for idx, item := range fields {
		if idx == len(fields)-1 {
			// the last item should be the filename
			// it is always preceded by a space
			if strings.HasPrefix(item, " ") {
				name := c.fromServerEncoding(item[1:])
				e.SetName(name)
			} else {
				err = fmt.Errorf("invalid filename %s", item)
				return
			}
		} else {
			// other items are facts, in the form "key=value"
			factKV := strings.Split(item, "=")
			if len(factKV) == 2 {
				e.Facts[strings.ToLower(factKV[0])] = factKV[1]
			}
		}
	}
	return
}

// MList issues an MLSD command, which lists a directory in a standard format
func (c *ServerConn) MList(path string) (entries []EntryEx, err error) {
	path = c.toServerEncoding(path)
	conn, err := c.cmdDataConnFrom(0, "MLSD %s", path)
	if err != nil {
		return
	}

	r := &response{conn, c}
	defer r.Close()

	bio := bufio.NewReader(r)
	for {
		line, e := bio.ReadString('\n')
		if e == io.EOF {
			break
		} else if e != nil {
			return nil, e
		}
		entry, err := c.parseMListLine(line)
		if err == nil && (entry.Name() != "." && entry.Name() != ".." || c.ListDotDirs) {
			entries = append(entries, entry)
		}
	}
	return
}

// MInfo issues an MLST command, which returns info about the specified directory entry
// in a standard format
func (c *ServerConn) MInfo(path string) (entry EntryEx, err error) {
	path = c.toServerEncoding(path)
	_, resp, err := c.cmd(StatusRequestedFileActionOK, "MLST %s", path)
	if err != nil {
		return
	}
	lines := strings.Split(resp, "\n")
	// RFC3659 section 7.2. (control-response) states that the response has 3 lines,
	// the second line contains one space and then the data.
	if len(lines) == 3 && len(lines[1]) > 1 {
		line := lines[1][1:]
		entry, err = c.parseMListLine(line)
	} else {
		err = fmt.Errorf("unexpected MLST response %s", resp)
	}
	return
}

// ChangeDir issues a CWD FTP command, which changes the current directory to
// the specified path.
func (c *ServerConn) ChangeDir(path string) error {
	path = c.toServerEncoding(path)
	_, _, err := c.cmd(StatusRequestedFileActionOK, "CWD %s", path)
	return err
}

// ChangeDirToParent issues a CDUP FTP command, which changes the current
// directory to the parent directory.  This is similar to a call to ChangeDir
// with a path set to "..".
func (c *ServerConn) ChangeDirToParent() error {
	_, _, err := c.cmd(StatusRequestedFileActionOK, "CDUP")
	return err
}

// CurrentDir issues a PWD FTP command, which Returns the path of the current
// directory.
func (c *ServerConn) CurrentDir() (string, error) {
	_, msg, err := c.cmd(StatusPathCreated, "PWD")
	if err != nil {
		return "", err
	}

	start := strings.Index(msg, "\"")
	end := strings.LastIndex(msg, "\"")

	if start == -1 || end == -1 {
		return "", errors.New("unsupported PWD response format")
	}

	return c.fromServerEncoding(msg[start+1 : end]), nil
}

// Retr issues a RETR FTP command to fetch the specified file from the remote
// FTP server.
//
// The returned ReadCloser must be closed to cleanup the FTP data connection.
func (c *ServerConn) Retr(path string) (io.ReadCloser, error) {
	return c.RetrFrom(path, 0)
}

// RetrFrom issues a RETR FTP command to fetch the specified file from the remote
// FTP server, the server will not send the offset first bytes of the file.
//
// The returned ReadCloser must be closed to cleanup the FTP data connection.
func (c *ServerConn) RetrFrom(path string, offset uint64) (io.ReadCloser, error) {
	path = c.toServerEncoding(path)
	conn, err := c.cmdDataConnFrom(offset, "RETR %s", path)
	if err != nil {
		return nil, err
	}

	r := &response{conn, c}
	return r, nil
}

// Stor issues a STOR FTP command to store a file to the remote FTP server.
// Stor creates the specified file with the content of the io.Reader.
//
// Hint: io.Pipe() can be used if an io.Writer is required.
func (c *ServerConn) Stor(path string, r io.Reader) error {
	return c.StorFrom(path, r, 0)
}

// StorFrom issues a STOR FTP command to store a file to the remote FTP server.
// Stor creates the specified file with the content of the io.Reader, writing
// on the server will start at the given file offset.
//
// Hint: io.Pipe() can be used if an io.Writer is required.
func (c *ServerConn) StorFrom(path string, r io.Reader, offset uint64) error {
	path = c.toServerEncoding(path)

	conn, err := c.cmdDataConnFrom(offset, "STOR %s", path)
	if err != nil {
		return err
	}

	_, err = io.Copy(conn, r)
	conn.Close()
	if err != nil {
		return err
	}

	_, _, err = c.conn.ReadCodeLine(StatusClosingDataConnection)
	return err
}

// Rename renames a file on the remote FTP server.
func (c *ServerConn) Rename(from, to string) error {
	from = c.toServerEncoding(from)
	to = c.toServerEncoding(to)

	_, _, err := c.cmd(StatusRequestFilePending, "RNFR %s", from)
	if err != nil {
		return err
	}

	_, _, err = c.cmd(StatusRequestedFileActionOK, "RNTO %s", to)
	return err
}

// Delete issues a DELE FTP command to delete the specified file from the
// remote FTP server.
func (c *ServerConn) Delete(path string) error {
	path = c.toServerEncoding(path)
	_, _, err := c.cmd(StatusRequestedFileActionOK, "DELE %s", path)
	return err
}

// MakeDir issues a MKD FTP command to create the specified directory on the
// remote FTP server.
func (c *ServerConn) MakeDir(path string) error {
	path = c.toServerEncoding(path)
	_, _, err := c.cmd(StatusPathCreated, "MKD %s", path)
	return err
}

// RemoveDir issues a RMD FTP command to remove the specified directory from
// the remote FTP server.
func (c *ServerConn) RemoveDir(path string) error {
	path = c.toServerEncoding(path)
	_, _, err := c.cmd(StatusRequestedFileActionOK, "RMD %s", path)
	return err
}

// NoOp issues a NOOP FTP command.
// NOOP has no effects and is usually used to prevent the remote FTP server to
// close the otherwise idle connection.
func (c *ServerConn) NoOp() error {
	_, _, err := c.cmd(StatusCommandOK, "NOOP")
	return err
}

// Logout issues a REIN FTP command to logout the current user.
func (c *ServerConn) Logout() error {
	_, _, err := c.cmd(StatusReady, "REIN") // from dsluis/goftp
	return err
}

// Quit issues a QUIT FTP command to properly close the connection from the
// remote FTP server.
func (c *ServerConn) Quit() error {
	c.conn.Cmd("QUIT")
	return c.conn.Close()
}

// The following functions implement the FileSystem interface

// ReadDir reads the directory named by dirname and returns a
// list of directory entries.
func (c *ServerConn) ReadDir(dirname string) (entries []os.FileInfo, err error) {
	dirEntries, err := c.MList(dirname)
	if err != nil {
		return
	}
	entries = make([]os.FileInfo, len(dirEntries))
	for i, fi := range dirEntries {
		entries[i] = fi
	}
	return
}

// Lstat returns a FileInfo describing the named file. If the file is a
// symbolic link, the returned FileInfo describes the symbolic link. Lstat
// makes no attempt to follow the link.
func (c *ServerConn) Lstat(name string) (entry os.FileInfo, err error) {
	entry, err = c.MInfo(name)
	return
}

// Join joins any number of path elements into a single path, adding a
// separator if necessary. The result is Cleaned; in particular, all
// empty strings are ignored.
//
// The separator is FileSystem specific.
func (c *ServerConn) Join(elem ...string) string {
	return path.Join(elem...)
}

// Read implements the io.Reader interface on a FTP data connection.
func (r *response) Read(buf []byte) (int, error) {
	n, err := r.conn.Read(buf)
	return n, err
}

// Close implements the io.Closer interface on a FTP data connection.
func (r *response) Close() error {
	err := r.conn.Close()
	_, _, err2 := r.c.conn.ReadResponse(StatusClosingDataConnection)
	if err2 != nil {
		err = err2
	}
	return err
}

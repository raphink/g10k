package main

import (
	"archive/tar"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/henvic/uiprogress"
	"github.com/klauspost/pgzip"
	"github.com/tidwall/gjson"
)

func doModuleInstallOrNothing(m string, fm ForgeModule) {
	ma := strings.Split(m, "-")
	moduleName := ma[0] + "-" + ma[1]
	moduleVersion := ma[2]
	workDir := config.ForgeCacheDir + m
	fr := ForgeResult{false, ma[2], "", 0}
	if check4update {
		moduleVersion = "latest"
	}
	if moduleVersion == "latest" {
		if !fileExists(workDir) {
			Debugf("" + workDir + " does not exist, fetching module")
			// check forge API what the latest version is
			fr = queryForgeAPI(moduleName, "false", fm)
			if fr.needToGet {
				if _, ok := uniqueForgeModules[moduleName+"-"+fr.versionNumber]; ok {
					Debugf("no need to fetch Forge module " + moduleName + " in latest, because latest is " + fr.versionNumber + " and that will already be fetched")
					fr.needToGet = false
					versionDir := config.ForgeCacheDir + moduleName + "-" + fr.versionNumber
					absolutePath, err := filepath.Abs(versionDir)
					Debugf("trying to create symlink " + workDir + " pointing to " + absolutePath)
					if err != nil {
						Fatalf("doModuleInstallOrNothing(): Error while resolving absolute file path for " + versionDir + " Error: " + err.Error())
					}
					if err := os.Symlink(absolutePath, workDir); err != nil {
						Fatalf("doModuleInstallOrNothing(): 1 Error while creating symlink " + workDir + " pointing to " + absolutePath + err.Error())
					}
					//} else {
					//Debugf("need to fetch Forge module " + moduleName + " in latest, because version " + fr.versionNumber + " will not be fetched already")

					//fmt.Println(needToGet)
				}
			}
		} else {
			if fm.cacheTtl > 0 {
				lastCheckedFile := workDir + "-last-checked"
				//Debugf("checking for " + lastCheckedFile)
				if fileInfo, err := os.Lstat(lastCheckedFile); err == nil {
					//Debugf("found " + lastCheckedFile + " with mTime " + fileInfo.ModTime().String())
					if fileInfo.ModTime().Add(fm.cacheTtl).After(time.Now()) {
						Debugf("No need to check forge API if latest version of module " + moduleName + " has been updated, because last-checked file " + lastCheckedFile + " is not older than " + fm.cacheTtl.String())
						// need to add the current (cached!) -latest version number to the latestForgeModules, because otherwise we would always sync this module, because 1.4.1 != -latest
						me := readModuleMetadata(workDir + "/metadata.json")
						latestForgeModules.Lock()
						latestForgeModules.m[moduleName] = me.version
						latestForgeModules.Unlock()
						return
					}
				}
			}
			// check forge API if latest version of this module has been updated
			Debugf("check forge API if latest version of module " + moduleName + " has been updated")
			// XXX: disable adding If-Modified-Since header for now
			// because then the latestForgeModules does not get set with the actual module version for latest
			// maybe if received 304 get the actual version from the -latest symlink
			fr = queryForgeAPI(moduleName, "false", fm)
			//fmt.Println(needToGet)
		}

	} else if moduleVersion == "present" {
		// ensure that a latest version this module exists
		latestDir := config.ForgeCacheDir + moduleName + "-latest"
		if !fileExists(latestDir) {
			if _, ok := uniqueForgeModules[moduleName+"-latest"]; ok {
				Debugf("we got " + m + ", but no " + latestDir + " to use, but -latest is already being fetched.")
				return
			}
			Debugf("we got " + m + ", but no " + latestDir + " to use. Getting -latest")
			doModuleInstallOrNothing(moduleName+"-latest", fm)
			return
		}
		Debugf("Nothing to do for module " + m + ", because " + latestDir + " exists")
	} else {
		if !fileExists(workDir) {
			fr.needToGet = true
		} else {
			Debugf("Using cache for " + moduleName + " in version " + moduleVersion + " because " + workDir + " exists")
			return
		}
	}

	//log.Println("fr.needToGet for ", m, fr.needToGet)

	if fr.needToGet {
		if ma[2] != "latest" {
			Debugf("Trying to remove: " + workDir)
			_ = os.Remove(workDir)
		} else {
			versionDir, _ := os.Readlink(workDir)
			if versionDir == config.ForgeCacheDir+moduleName+"-"+fr.versionNumber {
				Debugf("No reason to re-symlink again")
			} else {
				if fileExists(workDir) {
					Debugf("Trying to remove symlink: " + workDir)
					_ = os.Remove(workDir)
				}
				versionDir = config.ForgeCacheDir + moduleName + "-" + fr.versionNumber
				absolutePath, err := filepath.Abs(versionDir)
				if err != nil {
					Fatalf("doModuleInstallOrNothing(): Error while resolving absolute file path for " + versionDir + " Error: " + err.Error())
				}
				Debugf("trying to create symlink " + workDir + " pointing to " + absolutePath)
				if err := os.Symlink(absolutePath, workDir); err != nil {
					Fatalf("doModuleInstallOrNothing(): 2 Error while creating symlink " + workDir + " pointing to " + absolutePath + err.Error())
				}
			}
		}
		downloadForgeModule(moduleName, fr.versionNumber, fm, 1)
	}

}

func queryForgeAPI(name string, file string, fm ForgeModule) ForgeResult {
	//url := "https://forgeapi.puppetlabs.com:443/v3/modules/" + strings.Replace(name, "/", "-", -1)
	baseUrl := config.Forge.Baseurl
	if len(fm.baseUrl) > 0 {
		baseUrl = fm.baseUrl
	}
	//url := baseUrl + "/v3/modules?query=" + name
	url := baseUrl + "/v3/releases?module=" + name + "&owner=" + fm.author + "&sort_by=release_date&limit=1"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		Fatalf("queryForgeAPI(): Error creating GET request for Puppetlabs forge API" + err.Error())
	}
	if fileInfo, err := os.Stat(file); err == nil {
		Debugf("adding If-Modified-Since:" + string(fileInfo.ModTime().Format("Mon, 02 Jan 2006 15:04:05 GMT")) + " to Forge query")
		req.Header.Set("If-Modified-Since", fileInfo.ModTime().Format("Mon, 02 Jan 2006 15:04:05 GMT"))
	}
	req.Header.Set("User-Agent", "https://github.com/xorpaul/g10k/")
	req.Header.Set("Connection", "close")

	proxyURL, err := http.ProxyFromEnvironment(req)
	if err != nil {
		Fatalf("queryForgeAPI(): Error while getting http proxy with golang http.ProxyFromEnvironment()" + err.Error())
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	before := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		Fatalf("queryForgeAPI(): Error while issuing the HTTP request to " + url + " Error: " + err.Error())
	}
	duration := time.Since(before).Seconds()
	Verbosef("Querying Forge API " + url + " took " + strconv.FormatFloat(duration, 'f', 5, 64) + "s")

	mutex.Lock()
	syncForgeTime += duration
	mutex.Unlock()
	defer resp.Body.Close()

	if resp.Status == "200 OK" {
		// need to get latest version
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			Fatalf("queryForgeAPI(): Error while reading response body for Forge module " + fm.name + " from " + url + ": " + err.Error())
		}

		before := time.Now()
		currentRelease := gjson.Get(string(body), "results.0").Map()

		duration := time.Since(before).Seconds()
		version := currentRelease["version"].String()
		modulemd5sum := currentRelease["file_md5"].String()
		moduleFilesize := currentRelease["file_size"].Int()

		mutex.Lock()
		forgeJsonParseTime += duration
		mutex.Unlock()

		Debugf("found version " + version + " for " + name + "-latest")
		latestForgeModules.Lock()
		latestForgeModules.m[name] = version
		latestForgeModules.Unlock()

		lastCheckedFile := config.ForgeCacheDir + name + "-latest-last-checked"
		Debugf("writing last-checked file " + lastCheckedFile)
		f, _ := os.Create(lastCheckedFile)
		defer f.Close()

		return ForgeResult{true, version, modulemd5sum, moduleFilesize}

	} else if resp.Status == "304 Not Modified" {
		Debugf("Got 304 nothing to do for module " + name)
		return ForgeResult{false, "", "", 0}
	} else {
		Debugf("Unexpected response code " + resp.Status)
		return ForgeResult{false, "", "", 0}
	}
}

// getMetadataForgeModule queries the configured Puppet Forge and return
func getMetadataForgeModule(fm ForgeModule) ForgeModule {
	baseUrl := config.Forge.Baseurl
	if len(fm.baseUrl) > 0 {
		baseUrl = fm.baseUrl
	}
	url := baseUrl + "/v3/releases/" + fm.author + "-" + fm.name + "-" + fm.version
	req, err := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "https://github.com/xorpaul/g10k/")
	req.Header.Set("Connection", "close")
	proxyURL, err := http.ProxyFromEnvironment(req)
	if err != nil {
		Fatalf("getMetadataForgeModule(): Error while getting http proxy with golang http.ProxyFromEnvironment()" + err.Error())
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	before := time.Now()
	Debugf("GETing " + url)
	resp, err := client.Do(req)
	duration := time.Since(before).Seconds()
	Verbosef("GETing Forge metadata from " + url + " took " + strconv.FormatFloat(duration, 'f', 5, 64) + "s")
	mutex.Lock()
	syncForgeTime += duration
	mutex.Unlock()
	if err != nil {
		Fatalf("getMetadataForgeModule(): Error while querying metadata for Forge module " + fm.name + " from " + url + ": " + err.Error())
	}
	defer resp.Body.Close()

	if resp.Status == "200 OK" {
		body, err := ioutil.ReadAll(resp.Body)

		if err != nil {
			Fatalf("getMetadataForgeModule(): Error while reading response body for Forge module " + fm.name + " from " + url + ": " + err.Error())
		}

		before := time.Now()
		currentRelease := gjson.Parse(string(body)).Map()
		duration := time.Since(before).Seconds()
		modulemd5sum := currentRelease["file_md5"].String()
		moduleFilesize := currentRelease["file_size"].Int()
		Debugf("module: " + fm.author + "/" + fm.name + " modulemd5sum: " + modulemd5sum + " moduleFilesize: " + strconv.FormatInt(moduleFilesize, 10))

		mutex.Lock()
		forgeJsonParseTime += duration
		mutex.Unlock()

		return ForgeModule{md5sum: modulemd5sum, fileSize: moduleFilesize}
	} else {
		Fatalf("getMetadataForgeModule(): Unexpected response code while GETing " + url + " " + resp.Status)
	}
	return ForgeModule{}
}

func extractForgeModule(wgForgeModule *sync.WaitGroup, file *io.PipeReader, fileName string) {
	defer wgForgeModule.Done()
	funcName := funcName()

	before := time.Now()
	fileReader, err := pgzip.NewReader(file)

	unTar(fileReader, config.ForgeCacheDir+"/")

	if err != nil {
		Fatalf(funcName + "(): pgzip reader error for module " + fileName + " error:" + err.Error())
	}
	defer fileReader.Close()

	duration := time.Since(before).Seconds()
	Verbosef("Extracting " + config.ForgeCacheDir + fileName + " took " + strconv.FormatFloat(duration, 'f', 5, 64) + "s")
	mutex.Lock()
	ioForgeTime += duration
	mutex.Unlock()
}

func unTar(r io.Reader, targetBaseDir string) {
	funcName := funcName()
	tarBallReader := tar.NewReader(r)
	for {
		header, err := tarBallReader.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			Fatalf(funcName + "(): error while tar reader.Next() for io.Reader " + err.Error())
		}

		// get the individual filename and extract to the current directory
		filename := header.Name
		targetFilename := targetBaseDir + filename
		//Debugf("Trying to extract file" + filename)

		switch header.Typeflag {
		case tar.TypeDir:
			// handle directory
			//fmt.Println("Creating directory :", filename)
			//err = os.MkdirAll(targetFilename, os.FileMode(header.Mode)) // or use 0755 if you prefer
			err = os.MkdirAll(targetFilename, os.FileMode(0755)) // or use 0755 if you prefer

			if err != nil {
				Fatalf(funcName + "(): error while MkdirAll() " + filename + err.Error())
			}

		case tar.TypeReg:
			// handle normal file
			//fmt.Println("Untarring :", filename)
			writer, err := os.Create(targetFilename)

			if err != nil {
				Fatalf(funcName + "(): error while Create() " + filename + err.Error())
			}

			io.Copy(writer, tarBallReader)

			err = os.Chmod(targetFilename, os.FileMode(0644))

			if err != nil {
				Fatalf(funcName + "(): error while Chmod() " + filename + err.Error())
			}

			writer.Close()

		// Skip pax_global_header with the commit ID this archive was created from
		case tar.TypeXGlobalHeader:
			continue

		default:
			Fatalf(funcName + "(): Unable to untar type: " + string(header.Typeflag) + " in file " + filename)
		}
	}
}

func downloadForgeModule(name string, version string, fm ForgeModule, retryCount int) {
	funcName := funcName()
	var wgForgeModule sync.WaitGroup

	extractR, extractW := io.Pipe()
	saveFileR, saveFileW := io.Pipe()

	//url := "https://forgeapi.puppetlabs.com/v3/files/puppetlabs-apt-2.1.1.tar.gz"
	fileName := name + "-" + version + ".tar.gz"

	if !fileExists(config.ForgeCacheDir + name + "-" + version) {
		baseUrl := config.Forge.Baseurl
		if len(fm.baseUrl) > 0 {
			baseUrl = fm.baseUrl
		}
		url := baseUrl + "/v3/files/" + fileName
		req, err := http.NewRequest("GET", url, nil)
		req.Header.Set("User-Agent", "https://github.com/xorpaul/g10k/")
		req.Header.Set("Connection", "close")
		proxyURL, err := http.ProxyFromEnvironment(req)
		if err != nil {
			Fatalf(funcName + "(): Error while getting http proxy with golang http.ProxyFromEnvironment()" + err.Error())
		}
		client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
		before := time.Now()
		Debugf("GETing " + url)
		resp, err := client.Do(req)
		duration := time.Since(before).Seconds()
		Verbosef("GETing " + url + " took " + strconv.FormatFloat(duration, 'f', 5, 64) + "s")
		mutex.Lock()
		syncForgeTime += duration
		mutex.Unlock()
		if err != nil {
			Fatalf(funcName + "(): Error while GETing Forge module " + name + " from " + url + ": " + err.Error())
		}
		defer resp.Body.Close()

		if resp.Status == "200 OK" {
			wgForgeModule.Add(1)
			go func() {
				defer wgForgeModule.Done()
				Debugf(funcName + "(): Trying to create " + config.ForgeCacheDir + fileName)
				out, err := os.Create(config.ForgeCacheDir + fileName)
				if err != nil {
					Fatalf(funcName + "(): Error while creating file for Forge module " + config.ForgeCacheDir + fileName + " Error: " + err.Error())
				}
				defer out.Close()
				io.Copy(out, saveFileR)
				Debugf(funcName + "(): Finished creating " + config.ForgeCacheDir + fileName)
			}()
			wgForgeModule.Add(1)
			go extractForgeModule(&wgForgeModule, extractR, fileName)
			wgForgeModule.Add(1)
			go func() {
				defer wgForgeModule.Done()

				// after completing the copy, we need to close
				// the PipeWriters to propagate the EOF to all
				// PipeReaders to avoid deadlock
				defer extractW.Close()
				defer saveFileW.Close()

				var mw io.Writer
				// build the multiwriter for all the pipes
				mw = io.MultiWriter(extractW, saveFileW)

				// copy the data into the multiwriter
				if _, err := io.Copy(mw, resp.Body); err != nil {
					Fatalf("Error while writing to MultiWriter " + err.Error())
				}
			}()
		} else {
			Fatalf(funcName + "(): Unexpected response code while GETing " + url + " " + resp.Status)
		}
	} else {
		Debugf("Using cache for Forge module " + name + " version: " + version)
	}
	wgForgeModule.Wait()

	if checkSum || fm.sha256sum != "" {
		fm.version = version
		if doForgeModuleIntegrityCheck(fm) {
			if retryCount == 0 {
				Fatalf("downloadForgeModule(): giving up for Puppet module " + name + " version: " + version)
			}
			Warnf("Retrying...")
			purgeDir(config.ForgeCacheDir+fileName, "downloadForgeModule()")
			purgeDir(strings.Replace(config.ForgeCacheDir+fileName, ".tar.gz", "/", -1), "downloadForgeModule()")
			// retry if hash sum mismatch found
			downloadForgeModule(name, version, fm, retryCount-1)
		}
	}

}

// readModuleMetadata returns the Forgemodule struct of the given module file path
func readModuleMetadata(file string) ForgeModule {
	content, _ := ioutil.ReadFile(file)

	before := time.Now()
	name := gjson.Get(string(content), "name").String()
	version := gjson.Get(string(content), "version").String()
	author := gjson.Get(string(content), "author").String()
	duration := time.Since(before).Seconds()
	mutex.Lock()
	metadataJsonParseTime += duration
	mutex.Unlock()

	Debugf("Found in file " + file + " name: " + name + " version: " + version + " author: " + author)

	moduleName := "N/A"
	if strings.Contains(name, "-") {
		moduleName = strings.Split(name, "-")[1]
	} else {
		Debugf("Error: Something went wrong while decoding file " + file + " searching for the module name (found for name: " + name + "), version and author")
	}

	return ForgeModule{name: moduleName, version: version, author: strings.ToLower(author)}
}

func resolveForgeModules(modules map[string]ForgeModule) {
	if len(modules) <= 0 {
		Debugf("empty ForgeModule[] found, skipping...")
		return
	}
	var wgForge sync.WaitGroup
	bar := uiprogress.AddBar(len(modules)).AppendCompleted().PrependElapsed()
	bar.PrependFunc(func(b *uiprogress.Bar) string {
		return fmt.Sprintf("Resolving Forge modules (%d/%d)", b.Current(), len(modules))
	})
	for m, fm := range modules {
		wgForge.Add(1)
		go func(m string, fm ForgeModule, bar *uiprogress.Bar) {
			defer wgForge.Done()
			defer bar.Incr()
			Debugf("resolveForgeModules(): Trying to get forge module " + m + " with Forge base url " + fm.baseUrl + " and CacheTtl set to " + fm.cacheTtl.String())
			doModuleInstallOrNothing(m, fm)
		}(m, fm, bar)
	}
	wgForge.Wait()
}

func check4ForgeUpdate(moduleName string, currentVersion string, latestVersion string) {
	Verbosef("found currently deployed Forge module " + moduleName + " in version: " + currentVersion)
	Verbosef("found latest Forge module of " + moduleName + " in version: " + latestVersion)
	if currentVersion != latestVersion {
		color.Yellow("ATTENTION: Forge module: " + moduleName + " latest: " + latestVersion + " currently deployed: " + currentVersion)
		needSyncForgeCount++
	}
}

func doForgeModuleIntegrityCheck(m ForgeModule) bool {
	funcName := funcName()
	var wgCheckSum sync.WaitGroup

	wgCheckSum.Add(1)
	fmm := ForgeModule{}
	go func(m ForgeModule) {
		defer wgCheckSum.Done()
		fmm = getMetadataForgeModule(m)
		Debugf(funcName + "(): target md5 hash sum: " + fmm.md5sum)
		if m.sha256sum != "" {
			Debugf(funcName + "(): target sha256 hash sum from Puppetfile: " + m.sha256sum)
		}
	}(m)

	calculatedMd5Sum := ""
	calculatedSha256Sum := ""
	// http://rodaine.com/2015/04/async-split-io-reader-in-golang/
	// create the pipes
	md5R, md5W := io.Pipe()
	sha256R, sha256W := io.Pipe()
	var calculatedArchiveSize int64
	fileName := config.ForgeCacheDir + m.author + "-" + m.name + "-" + m.version + ".tar.gz"

	// md5 sum
	wgCheckSum.Add(1)
	go func(md5R *io.PipeReader) {
		defer wgCheckSum.Done()
		before := time.Now()
		hashmd5 := md5.New()
		if _, err := io.Copy(hashmd5, md5R); err != nil {
			Fatalf(funcName + "(): Error while reading Forge module archive " + fileName + " ! Error: " + err.Error())
		}
		duration := time.Since(before).Seconds()
		Verbosef("Calculating md5 sum for " + fileName + " took " + strconv.FormatFloat(duration, 'f', 5, 64) + "s")
		calculatedMd5Sum = hex.EncodeToString(hashmd5.Sum(nil))
		Debugf(funcName + "(): calculated md5 hash sum: " + calculatedMd5Sum)
	}(md5R)

	if m.sha256sum != "" {
		// sha256 sum
		wgCheckSum.Add(1)
		go func(sha256R *io.PipeReader) {
			defer wgCheckSum.Done()
			before := time.Now()
			hashSha256 := sha256.New()
			if _, err := io.Copy(hashSha256, sha256R); err != nil {
				Fatalf(funcName + "(): Error while reading Forge module archive " + fileName + " ! Error: " + err.Error())
			}
			duration := time.Since(before).Seconds()
			Verbosef("Calculating sha256 sum for " + fileName + " took " + strconv.FormatFloat(duration, 'f', 5, 64) + "s")
			calculatedSha256Sum = hex.EncodeToString(hashSha256.Sum(nil))
			Debugf(funcName + "(): calculated sha256 hash sum: " + calculatedSha256Sum)
		}(sha256R)
	}

	wgCheckSum.Add(1)
	go func() {
		defer wgCheckSum.Done()

		// after completing the copy, we need to close
		// the PipeWriters to propagate the EOF to all
		// PipeReaders to avoid deadlock
		defer md5W.Close()
		if m.sha256sum != "" {
			defer sha256W.Close()
		}

		var mw io.Writer
		if m.sha256sum != "" {
			// build the multiwriter for all the pipes
			mw = io.MultiWriter(md5W, sha256W)
		} else {
			mw = io.MultiWriter(md5W)
		}

		before := time.Now()
		if fi, err := os.Stat(fileName); err == nil {
			calculatedArchiveSize = fi.Size()
			file, err := os.Open(fileName)
			if err != nil {
				Fatalf("Can't access Forge module archive " + fileName + " ! Error: " + err.Error())
			}
			defer file.Close()

			// copy the data into the multiwriter
			if _, err := io.Copy(mw, file); err != nil {
				Fatalf("Error while writing to MultiWriter " + err.Error())
			}

		} else {
			Fatalf("Can't access Forge module archive " + fileName + " ! Error: " + err.Error())
		}
		duration := time.Since(before).Seconds()
		Verbosef("Calculating hash sum(s) for " + fileName + " took " + strconv.FormatFloat(duration, 'f', 5, 64) + "s")
		Debugf(funcName + "(): calculated archive size: " + strconv.FormatInt(calculatedArchiveSize, 10))
	}()

	wgCheckSum.Wait()

	if fmm.md5sum != calculatedMd5Sum {
		Warnf("WARNING: calculated md5sum " + calculatedMd5Sum + " for " + fileName + " does not match expected md5sum " + fmm.md5sum)
		return true
	} else {
		if m.sha256sum != calculatedSha256Sum {
			Warnf("WARNING: calculated sha256sum " + calculatedSha256Sum + " for " + fileName + " does not match expected sha256sum " + m.sha256sum)
			return true
		}
		if fmm.fileSize != calculatedArchiveSize {
			Warnf("WARNING: calculated file size " + strconv.FormatInt(calculatedArchiveSize, 10) + " for " + fileName + " does not match expected file size " + strconv.FormatInt(fmm.fileSize, 10))
			return true
		}
		Debugf("calculated file size " + strconv.FormatInt(calculatedArchiveSize, 10) + " for " + fileName + " does match expected file size " + strconv.FormatInt(fmm.fileSize, 10))
		Debugf("calculated md5sum " + calculatedMd5Sum + " for " + fileName + " does match expected md5sum " + fmm.md5sum)
		if m.sha256sum != "" {
			Debugf("calculated sha256sum " + calculatedSha256Sum + " for " + fileName + " does match expected sha256sum " + m.sha256sum)
		}
	}
	return false

}

func syncForgeToModuleDir(name string, m ForgeModule, moduleDir string) {
	funcName := funcName()
	mutex.Lock()
	syncForgeCount++
	mutex.Unlock()
	moduleName := strings.Replace(name, "/", "-", -1)
	//Debugf("m.name " + m.name + " m.version " + m.version + " moduleName " + moduleName)
	targetDir := moduleDir + m.name
	targetDir = checkDirAndCreate(targetDir, "as targetDir for module "+name)
	if m.version == "present" {
		if fileExists(targetDir + "metadata.json") {
			Debugf("Nothing to do, found existing Forge module: " + targetDir + "metadata.json")
			if check4update {
				me := readModuleMetadata(targetDir + "metadata.json")
				latestForgeModules.RLock()
				check4ForgeUpdate(m.name, me.version, latestForgeModules.m[moduleName])
				latestForgeModules.RUnlock()
			}
			return
		}
		// safe to do, because we ensured in doModuleInstallOrNothing() that -latest exists
		m.version = "latest"

	}
	if fileExists(targetDir + "metadata.json") {
		me := readModuleMetadata(targetDir + "metadata.json")
		if m.version == "latest" {
			latestForgeModules.RLock()
			if _, ok := latestForgeModules.m[moduleName]; ok {
				Debugf("using version " + latestForgeModules.m[moduleName] + " for " + moduleName + "-" + m.version)
				m.version = latestForgeModules.m[moduleName]
			}
			latestForgeModules.RUnlock()
		}
		if check4update {
			latestForgeModules.RLock()
			check4ForgeUpdate(m.name, me.version, latestForgeModules.m[moduleName])
			latestForgeModules.RUnlock()
		}
		if me.version == m.version {
			Debugf("Nothing to do, existing Forge module: " + targetDir + " has the same version " + me.version + " as the to be synced version: " + m.version)
			return
		}
		log.Println(funcName + "(): Need to sync, because existing Forge module: " + targetDir + " has version " + me.version + " and the to be synced version is: " + m.version)
		createOrPurgeDir(targetDir, " targetDir for module "+me.name)
	}
	workDir := config.ForgeCacheDir + moduleName + "-" + m.version + "/"
	if !fileExists(workDir) {
		Fatalf(funcName + "(): Forge module not found in dir: " + workDir)
	} else {
		Infof("Need to sync " + targetDir)
		mutex.Lock()
		needSyncForgeCount++
		mutex.Unlock()
		if !dryRun {
			hardlink := func(path string, info os.FileInfo, err error) error {
				if filepath.Base(path) != filepath.Base(workDir) { // skip the root dir
					target, err := filepath.Rel(workDir, path)
					if err != nil {
						Fatalf(funcName + "(): Can't make " + path + " relative to " + workDir + " Error: " + err.Error())
					}

					if info.IsDir() {
						//Debugf(funcName + "() Trying to mkdir " + targetDir + target)
						err = os.Mkdir(targetDir+target, os.FileMode(0755))
						if err != nil {
							Fatalf(funcName + "(): error while Mkdir() " + targetDir + target + " Error: " + err.Error())
						}
					} else {
						//Debugf(funcName + "() Trying to hardlink " + path + " to " + targetDir + target)
						err = os.Link(path, targetDir+target)
						if err != nil {
							Fatalf(funcName + "(): Failed to hardlink " + path + " to " + targetDir + target + " Error: " + err.Error())
						}
					}
				}
				return nil
			}
			c := make(chan error)
			Debugf(funcName + "() filepath.Walk'ing directory " + workDir)
			before := time.Now()
			go func() { c <- filepath.Walk(workDir, hardlink) }()
			<-c // Walk done
			duration := time.Since(before).Seconds()
			mutex.Lock()
			ioForgeTime += duration
			mutex.Unlock()
			Verbosef("Populating " + targetDir + " took " + strconv.FormatFloat(duration, 'f', 5, 64) + "s")
		}
	}
}

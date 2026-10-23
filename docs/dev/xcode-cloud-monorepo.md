# Xcode Cloud after the monorepo merge

On 2026-10-15 the iPhone app moved from the separate `kccarlos/herdr-sheepdog-ios`
repository into [`ios/`](../../ios/README.md) of this one, with its full
history. Xcode Cloud still builds from the old repository until its
workflows are switched. This page lists the steps, in order, for whoever
holds the Account Holder's App Store Connect login. The rest of the Xcode
Cloud setup (signing, TestFlight groups, costs, troubleshooting) is in
[`ios/docs/dev/xcode-cloud.md`](../../ios/docs/dev/xcode-cloud.md).

Since 2026-10-21 the app's tests run on GitHub Actions (`ios.yml`) and the
**PR** workflow below is gone from App Store Connect; only the TestFlight
workflows remain. The PR steps are kept as the record of the switch.

## What changed in the repository

| | Before | Now |
|---|---|---|
| Repository | `kccarlos/herdr-sheepdog-ios` | `kccarlos/herdr-sheepdog` |
| `project.yml` | repository root | `ios/project.yml` (its paths are relative to `ios/`, unchanged) |
| Generated project | `Sheepdog.xcodeproj` | `ios/Sheepdog.xcodeproj` (still generated, never committed) |
| Post-clone script | `ci_scripts/ci_post_clone.sh` | `ios/ci_scripts/ci_post_clone.sh`, next to the project |
| Release tags | `v0.1.0` … `v0.1.10` | `ios-v0.1.0` … `ios-v0.1.10`; plain `v*` tags are now **daemon** releases |

Xcode Cloud looks for `ci_scripts` in the directory that holds the project
or workspace, so it stays in `ios/`. The script now changes into
`$CI_PRIMARY_REPOSITORY_PATH/ios` before it runs XcodeGen, and on a tag
build it requires the tag to be `ios-v` + `MARKETING_VERSION`.

The current workflows (from `asc xcode-cloud workflows view`, 2026-10-15):

| Workflow | ID | Project | Start conditions | Action |
|---|---|---|---|---|
| PR | `B969BCEA-AC18-478B-A858-C8F1EA508D2F` | `Sheepdog.xcodeproj` | PRs into `main`; pushes to `main`; manual on any branch | Test, scheme Sheepdog |
| TestFlight | `C91F8A04-EBD8-4BD1-B363-00A81618F149` | `Sheepdog.xcodeproj` | tags beginning with `v` | Archive (TestFlight and App Store) |
| Internal TestFlight Build | `23908240-8E3A-467D-AAE5-880BE3748AC8` | `Sheepdog.xcodeproj` | manual on any branch | Archive |

**Switch the TestFlight workflow's tag rule as soon as it builds from the
new repository.** Until then the "begins with `v`" rule would match the
daemon's release tags.

## 1. Connect the repository (first, before any workflow change)

1. GitHub ▸ your account **Settings ▸ Applications ▸ Installed GitHub Apps ▸
   Xcode Cloud ▸ Configure ▸ Repository access**: add
   `kccarlos/herdr-sheepdog` (keep `herdr-sheepdog-ios` until step 4) ▸ Save.
2. App Store Connect ▸ **Apps ▸ Sheepdog for herdr ▸ Xcode Cloud ▸ Settings
   ▸ Repositories** (on older layouts "Source Code"): check that
   `kccarlos/herdr-sheepdog` is listed and has access. If it is not, use
   **Add Repository** (or **Grant Access**) there.
3. Check it from a terminal, if `asc` is set up: `asc xcode-cloud scm
   repositories list` should list `herdr-sheepdog` next to
   `herdr-sheepdog-ios`.

Xcode needs an HTTPS remote for this (an SSH host alias breaks it; see
"When something fails" in the iOS Xcode Cloud page).

## 2. Change each workflow

App Store Connect ▸ Sheepdog for herdr ▸ **Xcode Cloud ▸ Manage Workflows**
(or Xcode ▸ Report navigator ▸ Cloud ▸ right-click Sheepdog ▸ Manage
Workflows). For each workflow, **Edit Workflow**:

### PR

| Section | Change to |
|---|---|
| General | Repository **`kccarlos/herdr-sheepdog`**. Project **`ios/Sheepdog.xcodeproj`** (type the path: the project is generated, so the picker cannot see it; the "may only exist locally" warning is expected) |
| Start Conditions | **Pull Request Changes**: source Any, target `main`, **Files and Folders ▸ Start a build if any file changes ▸ directory `ios`**. **Branch Changes**: `main`, the same Files and Folders rule. Keep the manual start condition and Auto-cancel |
| Actions | unchanged (Test, scheme Sheepdog, one recent iPhone simulator) |
| Post-Actions | none |

### TestFlight

| Section | Change to |
|---|---|
| General | Repository **`kccarlos/herdr-sheepdog`**. Project **`ios/Sheepdog.xcodeproj`** |
| Start Conditions | **Tag Changes ▸ Custom Tags ▸ tag begins with `ios-v`**. No branch condition |
| Actions | unchanged (Archive, scheme Sheepdog; Deployment Preparation **TestFlight and App Store**, which external testing needs) |
| Post-Actions | **TestFlight External Testing ▸ group `Public`** and **TestFlight Internal Testing ▸ group `Me`** |

### Internal TestFlight Build

| Section | Change to |
|---|---|
| General | Repository **`kccarlos/herdr-sheepdog`**. Project **`ios/Sheepdog.xcodeproj`** |
| Start Conditions | unchanged (manual only) |
| Actions | unchanged |
| Post-Actions | **TestFlight Internal Testing ▸ group `Me`** (optional; without it, builds are added to a group by hand) |

If the workflow editor does not let you change the repository of an
existing workflow, create the workflow again: open
`herdr-sheepdog/ios/Sheepdog.xcodeproj` in Xcode (`cd ios && xcodegen
generate` first), **Integrate ▸ Xcode Cloud ▸ Create Workflow**, pick
`kccarlos/herdr-sheepdog`, enter the settings above, then disable and
delete the old one. The App Store Connect app record, signing and
TestFlight groups do not depend on the repository and stay as they are.

## 3. Verify

1. Start the **PR** workflow by hand on branch `main`. The log of "Run
   ci_post_clone.sh" should show XcodeGen generating the project in `ios/`,
   then the tests run.
2. Open a pull request that changes a file under `ios/`: Xcode Cloud reports
   its check on it. A pull request that changes only daemon files starts no
   Xcode Cloud build.
3. At the next app release, bump `MARKETING_VERSION` in `ios/project.yml`
   (the last tag is `ios-v0.1.10`) and push the tag `ios-v<version>`; the
   TestFlight build lands in Public and Me.

## 4. After it works

1. App Store Connect ▸ Xcode Cloud ▸ Settings ▸ Repositories: remove
   `kccarlos/herdr-sheepdog-ios`; on GitHub, drop it from the Xcode Cloud
   app's repository access.
2. Archive `kccarlos/herdr-sheepdog-ios` on GitHub (Settings ▸ General ▸
   Archive this repository). Its README already points here.

## What the API can and cannot do

`asc` (the App Store Connect API) can read all three workflows and can
update a workflow's project path and start conditions with
`asc xcode-cloud workflows update --id <ID> --file workflow.json`, once
step 1 is done. It cannot do the rest: in the public API a workflow's
repository is set only when the workflow is created, and TestFlight
post-actions (the Public and Me groups) are not part of the API's workflow
model. So do the switch in the App Store Connect or Xcode editor as above;
the API is useful to check the result (`asc xcode-cloud workflows view
--id <ID>` shows `containerFilePath` and the start conditions).

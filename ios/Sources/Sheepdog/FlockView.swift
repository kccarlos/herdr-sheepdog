import SheepdogKit
import SwiftUI

private struct FlockInputs: Equatable {
    var flock: Flock = Flock([])
    var computers: [ComputerSnapshot] = []
    var currentName = ""
    var voiceQuestionID: String?
    var collapsedComputers: Set<String> = []
    var question: (String) -> ApprovalFlow.Card? = { _ in nil }
    var answer: (String, Int) -> Void = { _, _ in }
    var onNewAgent: (() -> Void)?
    var remoteCard: (String, String) -> ApprovalFlow.Card? = { _, _ in nil }
    var talkTo: (String) -> Void = { _ in }
    var retry: (String) -> Void = { _ in }
    var toggleComputer: (String) -> Void = { _ in }
    var expandComputer: (String) -> Void = { _ in }

    static func == (lhs: FlockInputs, rhs: FlockInputs) -> Bool {
        lhs.flock == rhs.flock
            && lhs.computers == rhs.computers
            && lhs.currentName == rhs.currentName
            && lhs.voiceQuestionID == rhs.voiceQuestionID
            && lhs.collapsedComputers == rhs.collapsedComputers
            && (lhs.onNewAgent != nil) == (rhs.onNewAgent != nil)
    }
}

private struct FlockInputsKey: EnvironmentKey {
    static let defaultValue = FlockInputs()
}

private extension EnvironmentValues {
    var flockInputs: FlockInputs {
        get { self[FlockInputsKey.self] }
        set { self[FlockInputsKey.self] = newValue }
    }
}

/// The Agents page: one compact card per running agent, blocked first,
/// then "Not running (N)" folded at the bottom. A waiting agent's card
/// carries its question and answer buttons and never folds; the others
/// open on a tap to show their details. Mirrors `/api/state`'s agents,
/// refreshed over `/events`.
struct FlockView: View {
    let flock: Flock
    /// The question shown on an agent's card, if it has one open.
    let question: (String) -> ApprovalFlow.Card?
    /// A tap on an option: the question's id and the option's index.
    let answer: (String, Int) -> Void
    /// The cards the user opened (kept for the session).
    @Binding var expanded: Set<String>
    /// A notification tap: scroll to this agent's card, then forget it.
    @Binding var focusedAgent: AgentFocus?
    /// The empty state's New agent, when the daemon has agent actions.
    var onNewAgent: (() -> Void)?
    /// The voice's own question: its buttons keep the `approval.*` names.
    var voiceQuestionID: String?

    /// Several computers only: the other computers, each a section after
    /// this one's (app-redesign.md 5.2). Empty with one computer, and then
    /// the page is one list as it always was.
    var computers: [ComputerSnapshot] = []
    /// The current computer's name, for its section header.
    var currentName = ""
    /// Another computer's question on an agent's card: (computer, pane).
    var remoteCard: (String, String) -> ApprovalFlow.Card? = { _, _ in nil }
    var talkTo: (String) -> Void = { _ in }
    var retry: (String) -> Void = { _ in }
    /// Closed computer sections, owned by `AgentsModel`.
    var collapsedComputers: Set<String> = []
    var toggleComputer: (String) -> Void = { _ in }
    var expandComputer: (String) -> Void = { _ in }

    /// Closed by default: the running agents are what matters.
    @State private var showNotRunning = false

    private var inputs: FlockInputs {
        FlockInputs(
            flock: flock,
            computers: computers,
            currentName: currentName,
            voiceQuestionID: voiceQuestionID,
            collapsedComputers: collapsedComputers,
            question: question,
            answer: answer,
            onNewAgent: onNewAgent,
            remoteCard: remoteCard,
            talkTo: talkTo,
            retry: retry,
            toggleComputer: toggleComputer,
            expandComputer: expandComputer
        )
    }

    var body: some View {
        ScrollViewReader { proxy in
            FlockScrollContent(
                proxy: proxy,
                expanded: $expanded,
                focusedAgent: $focusedAgent,
                showNotRunning: $showNotRunning
            )
        }
        .environment(\.flockInputs, inputs)
        .accessibilityIdentifier("flock.list")
    }
}

private struct FlockScrollContent: View {
    let proxy: ScrollViewProxy
    @Binding var expanded: Set<String>
    @Binding var focusedAgent: AgentFocus?
    @Binding var showNotRunning: Bool
    @Environment(\.flockInputs) private var inputs

    private var flock: Flock { inputs.flock }
    private var computers: [ComputerSnapshot] { inputs.computers }
    private var currentName: String { inputs.currentName }
    private var voiceQuestionID: String? { inputs.voiceQuestionID }
    private var collapsedComputers: Set<String> { inputs.collapsedComputers }
    private var question: (String) -> ApprovalFlow.Card? { inputs.question }
    private var answer: (String, Int) -> Void { inputs.answer }
    private var onNewAgent: (() -> Void)? { inputs.onNewAgent }
    private var remoteCard: (String, String) -> ApprovalFlow.Card? { inputs.remoteCard }
    private var talkTo: (String) -> Void { inputs.talkTo }
    private var retry: (String) -> Void { inputs.retry }
    private var toggleComputer: (String) -> Void { inputs.toggleComputer }
    private var expandComputer: (String) -> Void { inputs.expandComputer }

    private var sectioned: Bool { computers.count > 1 }

    var body: some View {
        ScrollView {
            LazyVStack(spacing: 8) {
                if sectioned {
                    // The store's order: Talk to never moves a section.
                    ForEach(computers) { computer in
                        if computer.isCurrent {
                            currentSection
                        } else {
                            otherSection(computer)
                        }
                    }
                } else {
                    currentSection
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 10)
            .padding(.bottom, 12)
        }
        .onAppear { scrollToFocus(proxy) }
        .onChange(of: focusedAgent) { _, _ in scrollToFocus(proxy) }
        .onChange(of: renderedFocusToken) { _, _ in scrollToFocus(proxy) }
    }

    /// The current computer: its header (several computers only), then its
    /// running agents and the closed Not running row.
    @ViewBuilder
    private var currentSection: some View {
        let current = computers.first(where: \.isCurrent)
        let id = current?.id ?? "current"
        if sectioned {
            let marker = marker(for: current, id: id)
            ComputerSectionHeader(
                id: id,
                name: currentName,
                detail: nil,
                marker: marker,
                isExpanded: isExpanded(id),
                toggleExpanded: { withAnimation(Theme.ease) { toggleComputer(id) } },
                moveVoice: { talkTo(id) },
                retry: { retry(id) }
            )
            .padding(.top, headerTopPadding(id))
        }
        if isExpanded(id) {
            if flock.running.isEmpty {
                if sectioned {
                    Text("No agents running here.")
                        .font(.subheadline)
                        .foregroundStyle(Theme.muted)
                        .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                } else {
                    NoAgentsView(onNewAgent: onNewAgent)
                        .padding(.top, 40)
                }
            }
            ForEach(flock.sections) { section in
                AgentSectionHeader(group: section.group, count: section.agents.count)
                ForEach(section.agents) { agent in
                    card(agent, primary: isFirstQuestion(agent))
                }
            }
            if !flock.notRunning.isEmpty {
                notRunningRow
                if showNotRunning {
                    ForEach(flock.notRunning) { agent in
                        card(agent, primary: false)
                    }
                }
            }
        }
    }

    /// Space above a section header: none for the first section.
    private func headerTopPadding(_ id: String) -> CGFloat {
        computers.first?.id == id ? 0 : 14
    }

    /// One accent per screen: only the first open question gets the filled
    /// answer.
    private func isFirstQuestion(_ agent: Agent) -> Bool {
        flock.running.first { question($0.paneID) != nil }?.paneID == agent.paneID
    }

    private func card(_ agent: Agent, primary: Bool) -> some View {
        AgentCard(
            agent: agent,
            question: question(agent.paneID),
            primary: primary,
            voiceQuestion: question(agent.paneID)?.pending.id == voiceQuestionID,
            expanded: Binding(
                get: { expanded.contains(agent.paneID) },
                set: { open in
                    if open { expanded.insert(agent.paneID) } else { expanded.remove(agent.paneID) }
                }
            ),
            answer: answer
        )
        .id(agent.paneID)
    }

    /// Another computer: its header and voice marker, then its running agents
    /// (blocked first) with their questions, or one dashed row when it does
    /// not answer. Its last-known agents are not shown as if they were live.
    @ViewBuilder
    private func otherSection(_ computer: ComputerSnapshot) -> some View {
        let offline: Bool = { if case .offline = computer.reachability { return true }; return false }()
        let marker = marker(for: computer, id: computer.id)
        ComputerSectionHeader(
            id: computer.id,
            name: computer.name,
            detail: offline ? nil : computer.summary(),
            marker: marker,
            isExpanded: isExpanded(computer.id),
            toggleExpanded: { withAnimation(Theme.ease) { toggleComputer(computer.id) } },
            moveVoice: { talkTo(computer.id) },
            retry: { retry(computer.id) }
        )
        .padding(.top, headerTopPadding(computer.id))
        if isExpanded(computer.id) {
            if offline {
                OfflineComputerRow(name: computer.name, lastSeen: computer.lastSeen()) { retry(computer.id) }
            } else {
                let running = Flock(computer.agents).running
                ForEach(running, id: \.paneID) { agent in
                    AgentCard(
                        agent: agent,
                        question: remoteCard(computer.id, agent.paneID),
                        primary: false,
                        voiceQuestion: false,
                        showsWindowButton: false,
                        expanded: Binding(
                            get: { expanded.contains("\(computer.id)|\(agent.paneID)") },
                            set: { open in
                                let key = "\(computer.id)|\(agent.paneID)"
                                if open { expanded.insert(key) } else { expanded.remove(key) }
                            }
                        ),
                        answer: answer
                    )
                    .id("\(computer.id)|\(agent.paneID)")
                }
            }
        }
    }

    private func marker(for computer: ComputerSnapshot?, id: String) -> ComputerVoiceMarkerState {
        ComputerVoiceMarkerState.resolve(
            computerID: id,
            currentComputerID: computers.first(where: \.isCurrent)?.id ?? id,
            reachability: computer?.reachability ?? .online,
            showsComputers: sectioned
        )
    }

    private func isExpanded(_ id: String) -> Bool {
        !sectioned || !collapsedComputers.contains(id)
    }

    private var notRunningRow: some View {
        Button {
            withAnimation(Theme.ease) { showNotRunning.toggle() }
        } label: {
            HStack(spacing: 6) {
                Image(systemName: "chevron.right")
                    .font(.footnote.weight(.semibold))
                    .rotationEffect(.degrees(showNotRunning ? 90 : 0))
                Text("Not running (\(flock.notRunning.count))")
                    .font(.subheadline.weight(.medium))
                Spacer()
            }
            .foregroundStyle(Theme.muted)
            .frame(minHeight: 44)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityValue(showNotRunning ? "Shown" : "Hidden")
        .accessibilityHint("Agents whose pane closed or that herdr no longer reports")
        .accessibilityIdentifier("flock.notRunning")
    }

    private var renderedFocusToken: [String] {
        var ids = flock.all.map(\.paneID)
        for computer in computers where !computer.isCurrent {
            if case .offline = computer.reachability { continue }
            ids.append(contentsOf: Flock(computer.agents).running.map { "\(computer.id)|\($0.paneID)" })
        }
        return ids
    }

    private func scrollToFocus(_ proxy: ScrollViewProxy) {
        guard let target = focusedAgent else { return }
        let currentID = computers.first(where: \.isCurrent)?.id
        let isOther = target.isOtherComputer(currentComputerID: currentID)
        if isOther, let cid = target.computerID {
            guard sectioned else { return }
            expandComputer(cid)
        } else if let currentID {
            expandComputer(currentID)
        }
        guard target.isTargetPresent(
            currentComputerID: currentID,
            sectioned: sectioned,
            currentAgents: flock.all,
            otherComputerAgents: { cid in
                guard let comp = computers.first(where: { $0.id == cid }) else { return nil }
                if case .offline = comp.reachability { return nil }
                return comp.agents
            }
        ) else { return }
        let scrollID: String
        if isOther, let cid = target.computerID {
            let key = "\(cid)|\(target.paneID)"
            if remoteCard(cid, target.paneID) == nil { expanded.insert(key) }
            scrollID = key
        } else {
            if flock.notRunning.contains(where: { $0.paneID == target.paneID }) { showNotRunning = true }
            if question(target.paneID) == nil { expanded.insert(target.paneID) }
            scrollID = target.paneID
        }
        DispatchQueue.main.async {
            guard focusedAgent == target else { return }
            withAnimation { proxy.scrollTo(scrollID, anchor: .top) }
            focusedAgent = nil
        }
    }
}

/// "Needs you", "Working" or "Done" over its agents. VoiceOver reaches it
/// with the headings rotor.
struct AgentSectionHeader: View {
    let group: AgentGroup
    let count: Int

    var body: some View {
        HStack(spacing: 6) {
            Text(group.title)
                .font(.footnote.weight(.semibold))
                .textCase(.uppercase)
                .foregroundStyle(group == .needsYou ? Theme.blockedText : Theme.muted)
            Text("\(count)")
                .font(.footnote.monospacedDigit())
                .foregroundStyle(Theme.muted)
            Spacer(minLength: 0)
        }
        .padding(.top, 6)
        .padding(.leading, 4)
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isHeader)
        .accessibilityIdentifier("flock.section.\(group.rawValue)")
    }
}

/// No agent is running: the sleeping mascot and the one next step.
struct NoAgentsView: View {
    var onNewAgent: (() -> Void)?

    var body: some View {
        VStack(spacing: 12) {
            MascotCard(name: "MascotSleeping", width: 220)
                .frame(height: 120)
            Text("No agents running")
                .font(.title2.weight(.bold))
                .fontDesign(.rounded)
                .foregroundStyle(Theme.ink)
                .padding(.top, 8)
            Text(onNewAgent == nil
                 ? "Start one in herdr on your computer, or ask Sheepdog to start one for you."
                 : "Start one with +, or ask Sheepdog to start one for you.")
                .font(.body)
                .foregroundStyle(Theme.muted)
                .multilineTextAlignment(.center)
                .fixedSize(horizontal: false, vertical: true)
            if let onNewAgent {
                Button(action: onNewAgent) {
                    Label("New agent", systemImage: "plus")
                        .font(.body.weight(.semibold))
                        .foregroundStyle(Theme.ink)
                        .padding(.horizontal, 20)
                        .frame(minHeight: 44)
                        .overlay(Capsule().strokeBorder(Theme.ink, lineWidth: 1.5))
                        .contentShape(Capsule())
                }
                .buttonStyle(.plain)
                .padding(.top, 6)
                .accessibilityIdentifier("flock.empty.newAgent")
            }
        }
        .padding(.horizontal, 16)
        .frame(maxWidth: .infinity)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("flock.empty")
    }
}

/// One agent (canvas "AgentCard"): a dot, the name, the kind and the state
/// with its time; then the task; and the window button. A tap opens the
/// details (working on, last result, the newest journal entry, the last
/// answered question, the space). A waiting agent shows its question
/// instead and never folds.
struct AgentCard: View {
    let agent: Agent
    let question: ApprovalFlow.Card?
    var primary = true
    var voiceQuestion = true
    /// iPad: the expanded card also lists up to 3 earlier messages.
    var showsEarlier = false
    /// Another computer's agent has no window here (Talk to that computer).
    var showsWindowButton = true
    @Binding var expanded: Bool
    let answer: (String, Int) -> Void

    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.openAgentWindow) private var openWindow
    @EnvironmentObject private var model: SessionViewModel
    @State private var browser: BrowserTab?

    private var waiting: Bool { question != nil }
    /// This computer's own agent: pin and unread are written here only.
    private var controllable: Bool { showsWindowButton && agent.hasAttention }
    private var showsDetails: Bool { !waiting && expanded }
    private var tint: Color { Theme.color(for: agent.state) }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .top, spacing: 10) {
                Button {
                    guard !waiting else { return }
                    withAnimation(Theme.ease(reduceMotion: reduceMotion)) { expanded.toggle() }
                } label: {
                    summary
                }
                .buttonStyle(.plain)
                .accessibilityLabel(summaryLabel)
                .accessibilityValue(AgentAttentionWords.value(agent))
                .accessibilityHint(waiting ? "" : (expanded ? "Hides the details" : "Shows the details"))
                .accessibilityAddTraits(waiting ? [] : .isButton)
                .accessibilityIdentifier("flock.agent.\(agent.alias).toggle")
                if showsWindowButton { windowButton }
            }
            if let question {
                if agent.isMuted { heldNotice }
                ApprovalCardView(
                    card: question,
                    embedded: true,
                    primary: primary,
                    idPrefix: voiceQuestion ? "approval" : "question.\(agent.alias)",
                    answer: { answer(question.pending.id, $0) }
                )
            }
            if showsDetails {
                details
                    .transition(reduceMotion ? .opacity : .opacity.combined(with: .move(edge: .top)))
            }
        }
        .padding(.leading, 14)
        .padding([.top, .bottom, .trailing], 12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous).fill(Theme.card))
        .overlay(
            RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous)
                .strokeBorder(borderColor, lineWidth: waiting && primary ? 1.5 : 1)
        )
        // The window zooms out of the whole card.
        .zoomSource(agent.paneID)
        // The unread dot sits in the card's left margin, as in Mail.
        .overlay(alignment: .topLeading) {
            if agent.isUnread {
                Circle().fill(Color.accentColor).frame(width: 8, height: 8)
                    .padding(.leading, 3)
                    .padding(.top, 18)
                    .accessibilityHidden(true)
                    .accessibilityIdentifier("flock.agent.\(agent.alias).unread")
            }
        }
        .contextMenu {
            if controllable { attentionActions }
        }
        // The card looks seen while it is open on a screen that is in front.
        .modifier(SeenWhileVisible(paneID: agent.paneID, visible: expanded && controllable, attention: agent.attention))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("flock.agent.\(agent.alias)")
        .sheet(item: $browser) { tab in
            BrowserSheet(
                target: BrowserTarget(
                    title: agent.displayName, subtitle: agent.workspaceLabel?.trimmedOrNil,
                    scope: .agent(paneID: agent.paneID), changesPane: agent.paneID, folder: agent.cwd,
                    filesAvailable: model.filesAvailable
                ),
                services: model.browserServices(scope: .agent(paneID: agent.paneID), changesPane: agent.paneID),
                start: tab
            )
            .presentationSizing(.page)
        }
    }

    /// Pin / Unpin and Mark as unread: the card's context menu and the
    /// expanded card's buttons.
    @ViewBuilder
    private var attentionActions: some View {
        Button {
            model.setPinned(agent, !agent.isPinned)
        } label: {
            Label(agent.isPinned ? "Unpin" : "Pin to top", systemImage: agent.isPinned ? "pin.slash" : "pin")
        }
        .accessibilityIdentifier("flock.agent.\(agent.alias).pin")
        if agent.group == .done, !agent.isUnread {
            Button {
                model.markUnread(agent)
            } label: {
                Label("Mark as unread", systemImage: "circle.fill")
            }
            .accessibilityIdentifier("flock.agent.\(agent.alias).markUnread")
        }
    }

    /// A question from a muted space is not announced: say so, and that
    /// answering here works as always.
    private var heldNotice: some View {
        Label("Muted space. Sheepdog did not announce this, so answer it here.", systemImage: "bell.slash")
            .font(.footnote)
            .foregroundStyle(Theme.muted)
            .fixedSize(horizontal: false, vertical: true)
            .padding(.leading, 15)
            .accessibilityIdentifier("flock.agent.\(agent.alias).held")
    }

    private var borderColor: Color {
        guard waiting else { return Theme.line }
        return primary ? Color.accentColor : Theme.blocked
    }

    private var summaryLabel: String {
        var parts = [agent.alias]
        if let kind = agent.kind?.trimmedOrNil { parts.append(kind) }
        parts.append(waiting ? "waiting on you" : Theme.word(for: agent.state))
        if let line = agent.cardLine { parts.append(line) }
        return parts.joined(separator: ", ")
    }

    // MARK: - Collapsed

    private var summary: some View {
        VStack(alignment: .leading, spacing: 3) {
            if dynamicTypeSize.isAccessibilitySize {
                // Stacked: nothing is cut to "au…".
                HStack(spacing: 7) { dot; name }
                kindText
                stateText
            } else {
                // One line while it fits; else the state goes under the
                // name rather than cutting either.
                ViewThatFits(in: .horizontal) {
                    HStack(alignment: .firstTextBaseline, spacing: 7) {
                        dot
                        name.fixedSize()
                        kindText.fixedSize()
                        Spacer(minLength: 4)
                        stateText.fixedSize()
                    }
                    VStack(alignment: .leading, spacing: 2) {
                        HStack(alignment: .firstTextBaseline, spacing: 7) { dot; name; kindText }
                        stateText.padding(.leading, 15)
                    }
                }
            }
            if let line = agent.cardLine {
                HStack(spacing: 4) {
                    Text(line)
                        .font(.subheadline)
                        .foregroundStyle(Theme.muted)
                        .lineLimit(showsDetails || dynamicTypeSize.isAccessibilitySize ? 3 : 1)
                        .multilineTextAlignment(.leading)
                    if !waiting {
                        Image(systemName: showsDetails ? "chevron.up" : "chevron.down")
                            .font(.system(size: 11, weight: .bold))
                            .foregroundStyle(Theme.muted)
                    }
                }
                .padding(.leading, 15)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .contentShape(Rectangle())
    }

    private var dot: some View {
        Circle().fill(tint).frame(width: 8, height: 8)
            .doneFlash(agent.state)
            .animation(Theme.ease, value: agent.state)
            .alignmentGuide(.firstTextBaseline) { $0[.bottom] - 1 }
    }

    private var name: some View {
        HStack(spacing: 5) {
            Text(agent.alias)
                .font(.body.weight(.semibold))
                .foregroundStyle(Theme.ink)
                .lineLimit(dynamicTypeSize.isAccessibilitySize ? 2 : 1)
            // Said by the card's VoiceOver value, so the glyphs are silent.
            if agent.isPinned {
                Image(systemName: "pin.fill")
                    .font(.caption)
                    .foregroundStyle(Theme.muted)
                    .accessibilityHidden(true)
                    .accessibilityIdentifier("flock.agent.\(agent.alias).pinned")
            }
            if agent.isMuted {
                Image(systemName: "bell.slash.fill")
                    .font(.caption)
                    .foregroundStyle(Theme.muted)
                    .accessibilityHidden(true)
                    .accessibilityIdentifier("flock.agent.\(agent.alias).muted")
            }
        }
    }

    @ViewBuilder
    private var kindText: some View {
        if let kind = agent.kind?.trimmedOrNil {
            Text(kind)
                .font(.caption)
                .foregroundStyle(Theme.muted)
        }
    }

    private var stateText: some View {
        // Re-evaluated twice a minute, so "4 min" keeps up on its own.
        TimelineView(.periodic(from: .now, by: 30)) { context in
            let since = agent.timeInState(at: DemoClock.shifted(context.date))
            let word = Theme.word(for: agent.state)
            Text(since.map { "\(word) · \($0)" } ?? word)
                .font(.footnote.weight(waiting ? .semibold : .medium))
                .foregroundStyle(Theme.textColor(for: agent.state))
                .contentTransition(.numericText())
                .animation(Theme.ease, value: since)
                // Wraps at the accessibility sizes, where "Blocked · just
                // now" does not fit a line; never cut there.
                .lineLimit(dynamicTypeSize.isAccessibilitySize ? nil : 1)
                .fixedSize(horizontal: false, vertical: dynamicTypeSize.isAccessibilitySize)
                .accessibilityIdentifier("flock.agent.\(agent.alias).state")
        }
    }

    /// The round window button: opens the agent's live terminal.
    private var windowButton: some View {
        Button {
            openWindow(.agent(agent.paneID))
        } label: {
            Image(systemName: "apple.terminal")
                .font(.system(size: 16, weight: .medium))
                .foregroundStyle(Theme.ink)
                .frame(width: 36, height: 36)
                .background(Circle().fill(Theme.fill))
                .overlay(Circle().strokeBorder(Theme.line, lineWidth: 1))
                .frame(width: 44, height: 44)
                .contentShape(Circle())
                .hoverEffect()
        }
        .buttonStyle(.plain)
        .padding(.top, -6)
        .padding(.trailing, -4)
        .accessibilityLabel("Open \(agent.alias)'s window")
        .accessibilityHint("Shows the live terminal; you can type or press keys in it")
        .accessibilityIdentifier("flock.agent.\(agent.alias).window")
    }

    // MARK: - Expanded

    /// This computer's own agent, on a daemon that has the file browser.
    private var showsBrowse: Bool { showsWindowButton && model.filesAvailable }

    /// The way into the agent's files and changes (design: EntryCard), from
    /// the details, where the user is reading what the agent did.
    private var browseButton: some View {
        Button { browser = .files } label: {
            Label("Browse files", systemImage: "folder")
                .font(.subheadline.weight(.medium))
                .frame(minHeight: 44)
        }
        .buttonStyle(.plain)
        .foregroundStyle(Theme.primaryText)
        .accessibilityHint("Opens this agent's folder and what it changed, to read")
        .accessibilityIdentifier("flock.agent.\(agent.alias).files")
    }

    private var details: some View {
        VStack(alignment: .leading, spacing: 10) {
            if agent.state == .working, let task = agent.currentTask?.trimmedOrNil {
                labelled("Working on", task)
            }
            if let result = agent.lastResult, let spoken = result.spoken?.trimmedOrNil {
                VStack(alignment: .leading, spacing: 2) {
                    labelled(result.at.map { "Last result · \($0.formatted(date: .omitted, time: .shortened))" } ?? "Last result", spoken)
                    if let detail = result.detail?.trimmedOrNil {
                        DisclosureGroup("Detail") {
                            MarkdownText(detail)
                                .font(.footnote)
                                .foregroundStyle(Theme.muted)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .textSelection(.enabled)
                        }
                        .font(.footnote.weight(.semibold))
                        .tint(Theme.primaryText)
                    }
                }
            }
            journal
            if showsEarlier { earlier }
            if controllable {
                // Side by side; stacked at the accessibility sizes, where two
                // labels would break inside their words.
                let rowLayout = dynamicTypeSize.isAccessibilitySize
                    ? AnyLayout(VStackLayout(alignment: .leading, spacing: 0))
                    : AnyLayout(HStackLayout(spacing: 10))
                rowLayout {
                    Button {
                        model.setPinned(agent, !agent.isPinned)
                    } label: {
                        Label(agent.isPinned ? "Unpin" : "Pin to top", systemImage: agent.isPinned ? "pin.slash" : "pin")
                            .font(.subheadline.weight(.medium))
                            .frame(minHeight: 44)
                    }
                    .accessibilityIdentifier("flock.agent.\(agent.alias).pinButton")
                    if agent.group == .done, !agent.isUnread {
                        Button {
                            model.markUnread(agent)
                        } label: {
                            Label("Mark as unread", systemImage: "circle")
                                .font(.subheadline.weight(.medium))
                                .frame(minHeight: 44)
                        }
                        .accessibilityIdentifier("flock.agent.\(agent.alias).markUnreadButton")
                    }
                }
                .buttonStyle(.plain)
                .foregroundStyle(Theme.primaryText)
                if let attention = agent.attention, !attention.shared {
                    Text("Seen and pinned on this computer only")
                        .font(.caption)
                        .foregroundStyle(Theme.muted)
                        .accessibilityIdentifier("flock.agent.\(agent.alias).localOnly")
                }
            }
            if showsBrowse { browseButton }
            if let answered = agent.answeredQuestion {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Last question (answered)")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(Theme.muted)
                    Text(answered)
                        .font(.subheadline)
                        .foregroundStyle(Theme.muted)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .accessibilityElement(children: .combine)
            }
            if let place = agent.workspaceLabel?.trimmedOrNil {
                Label(place, systemImage: "folder")
                    .font(.caption)
                    .foregroundStyle(Theme.muted)
                    .accessibilityLabel("Space: \(place)")
            }
        }
        .padding(.top, 10)
        .padding(.leading, 15)
        .overlay(alignment: .top) { Rectangle().fill(Theme.line).frame(height: 1) }
    }

    /// The newest journal entry: what the worker wrote, the files it
    /// touched and its tools. Without one yet, say so.
    @ViewBuilder
    private var journal: some View {
        if let entry = agent.lastWorkerMessage, let message = entry.lastAssistant?.trimmedOrNil {
            VStack(alignment: .leading, spacing: 6) {
                Text(journalTitle(entry))
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Theme.muted)
                MarkdownText(message)
                    .font(.subheadline)
                    .foregroundStyle(Theme.ink)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
                let files = entry.filesTouched ?? []
                let tools = toolsLine(entry)
                if !files.isEmpty || tools != nil {
                    OptionFlow(spacing: 5) {
                        ForEach(files, id: \.self) { file in
                            Text((file as NSString).lastPathComponent)
                                .font(.caption.monospaced())
                                .foregroundStyle(Theme.muted)
                                .padding(.horizontal, 7)
                                .padding(.vertical, 2)
                                .background(RoundedRectangle(cornerRadius: 6).fill(Theme.card))
                                .overlay(RoundedRectangle(cornerRadius: 6).strokeBorder(Theme.line, lineWidth: 1))
                        }
                        if let tools {
                            Text(tools)
                                .font(.caption)
                                .foregroundStyle(Theme.muted)
                                .padding(.vertical, 2)
                        }
                    }
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 10)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(RoundedRectangle(cornerRadius: 12, style: .continuous).fill(Theme.fill))
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("flock.agent.\(agent.alias).message")
        } else if agent.awaitsFirstMessage {
            Text("Nothing from \(agent.alias) yet. Its first message shows here when it stops.")
                .font(.footnote)
                .foregroundStyle(Theme.muted)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    /// iPad: the worker's messages before the newest, newest first, one
    /// line each.
    @ViewBuilder
    private var earlier: some View {
        let entries = agent.earlierWorkerMessages()
        if !entries.isEmpty {
            VStack(alignment: .leading, spacing: 4) {
                Text("Earlier")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Theme.muted)
                ForEach(Array(entries.enumerated()), id: \.offset) { _, entry in
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text([entry.at?.formatted(date: .omitted, time: .shortened), entry.event?.trimmedOrNil].compactMap { $0 }.joined(separator: " · "))
                            .font(.footnote.monospacedDigit())
                            .foregroundStyle(Theme.muted)
                            .fixedSize()
                        Text(entry.lastAssistant?.trimmedOrNil ?? "")
                            .font(.footnote)
                            .foregroundStyle(Theme.ink)
                            .lineLimit(dynamicTypeSize.isAccessibilitySize ? 3 : 1)
                    }
                    .accessibilityElement(children: .combine)
                }
            }
            .accessibilityIdentifier("flock.agent.\(agent.alias).earlier")
        }
    }

    private func journalTitle(_ entry: Agent.JournalEntry) -> String {
        var title = "\(agent.alias) wrote"
        if let event = entry.event?.trimmedOrNil { title += " when \(event)" }
        if let at = entry.at { title += " · \(at.formatted(date: .omitted, time: .shortened))" }
        return title
    }

    private func toolsLine(_ entry: Agent.JournalEntry) -> String? {
        guard let tools = entry.tools, !tools.isEmpty else { return nil }
        return tools.sorted { $0.value == $1.value ? $0.key < $1.key : $0.value > $1.value }
            .map { "\($0.key) ×\($0.value)" }
            .joined(separator: " · ")
    }

    private func labelled(_ title: String, _ text: String) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(title)
                .font(.caption.weight(.semibold))
                .foregroundStyle(Theme.muted)
            MarkdownText(text)
                .font(.body)
                .foregroundStyle(Theme.ink)
                .fixedSize(horizontal: false, vertical: true)
                .textSelection(.enabled)
        }
        .accessibilityElement(children: .combine)
    }
}

extension String {
    /// Trimmed, or nil when nothing is left.
    var trimmedOrNil: String? {
        let t = trimmingCharacters(in: .whitespacesAndNewlines)
        return t.isEmpty ? nil : t
    }
}

/// What VoiceOver adds to an agent: "Unread", "Pinned", "Muted space".
enum AgentAttentionWords {
    static func value(_ agent: Agent) -> String {
        var words: [String] = []
        if agent.isUnread { words.append("Unread") }
        if agent.isPinned { words.append("Pinned") }
        if agent.isMuted { words.append("Muted space") }
        return words.joined(separator: ", ")
    }
}

/// Marks an agent seen while it is on screen: its window is open or its
/// card is expanded, and the app is in front. `SeenTracker` writes once per
/// turn, so a window left open marks each new turn as it ends.
struct SeenWhileVisible: ViewModifier {
    let paneID: String
    let visible: Bool
    /// Read so a new turn (or a mark that changed) looks again.
    let attention: AgentAttention?

    @EnvironmentObject private var model: SessionViewModel
    @Environment(\.scenePhase) private var scenePhase

    func body(content: Content) -> some View {
        content
            .task(id: Key(visible: visible, turn: attention?.turn, seen: attention?.seen, phase: scenePhase == .active)) {
                guard visible else { return }
                model.markVisible([paneID], appActive: scenePhase == .active)
            }
    }

    private struct Key: Equatable {
        var visible: Bool
        var turn: Int?
        var seen: Int?
        var phase: Bool
    }
}

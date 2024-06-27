package http

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"
	"github.com/rs/xid"

	"github.com/netbirdio/netbird/management/server"
	"github.com/netbirdio/netbird/management/server/http/api"
	"github.com/netbirdio/netbird/management/server/http/util"
	"github.com/netbirdio/netbird/management/server/jwtclaims"
	"github.com/netbirdio/netbird/management/server/status"
)

// Policies is a handler that returns policy of the account
type Policies struct {
	accountManager  server.AccountManager
	claimsExtractor *jwtclaims.ClaimsExtractor
}

// NewPoliciesHandler creates a new Policies handler
func NewPoliciesHandler(accountManager server.AccountManager, authCfg AuthCfg) *Policies {
	return &Policies{
		accountManager: accountManager,
		claimsExtractor: jwtclaims.NewClaimsExtractor(
			jwtclaims.WithAudience(authCfg.Audience),
			jwtclaims.WithUserIDClaim(authCfg.UserIDClaim),
		),
	}
}

// GetAllPolicies list for the account
func (h *Policies) GetAllPolicies(w http.ResponseWriter, r *http.Request) {
	claims := h.claimsExtractor.FromRequestContext(r)
	account, user, err := h.accountManager.GetAccountFromToken(claims)
	if err != nil {
		util.WriteError(err, w)
		return
	}

	accountPolicies, err := h.accountManager.ListPolicies(account.Id, user.Id)
	if err != nil {
		util.WriteError(err, w)
		return
	}

	policies := []*api.Policy{}
	for _, policy := range accountPolicies {
		resp := toPolicyResponse(account, policy)
		if len(resp.Rules) == 0 {
			util.WriteError(status.Errorf(status.Internal, "no rules in the policy"), w)
			return
		}
		policies = append(policies, resp)
	}

	util.WriteJSONObject(w, policies)
}

// UpdatePolicy handles update to a policy identified by a given ID
func (h *Policies) UpdatePolicy(w http.ResponseWriter, r *http.Request) {
	claims := h.claimsExtractor.FromRequestContext(r)
	account, user, err := h.accountManager.GetAccountFromToken(claims)
	if err != nil {
		util.WriteError(err, w)
		return
	}

	vars := mux.Vars(r)
	policyID := vars["policyId"]
	if len(policyID) == 0 {
		util.WriteError(status.Errorf(status.InvalidArgument, "invalid policy ID"), w)
		return
	}

	policyIdx := -1
	for i, policy := range account.Policies {
		if policy.ID == policyID {
			policyIdx = i
			break
		}
	}
	if policyIdx < 0 {
		util.WriteError(status.Errorf(status.NotFound, "couldn't find policy id %s", policyID), w)
		return
	}

	h.savePolicy(w, r, account, user, policyID)
}

// CreatePolicy handles policy creation request
func (h *Policies) CreatePolicy(w http.ResponseWriter, r *http.Request) {
	claims := h.claimsExtractor.FromRequestContext(r)
	account, user, err := h.accountManager.GetAccountFromToken(claims)
	if err != nil {
		util.WriteError(err, w)
		return
	}

	h.savePolicy(w, r, account, user, "")
}

// savePolicy handles policy creation and update
func (h *Policies) savePolicy(
	w http.ResponseWriter,
	r *http.Request,
	account *server.Account,
	user *server.User,
	policyID string,
) {
	var req api.PutApiPoliciesPolicyIdJSONRequestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		util.WriteErrorResponse("couldn't parse JSON request", http.StatusBadRequest, w)
		return
	}

	if req.Name == "" {
		util.WriteError(status.Errorf(status.InvalidArgument, "policy name shouldn't be empty"), w)
		return
	}

	if len(req.Rules) == 0 {
		util.WriteError(status.Errorf(status.InvalidArgument, "policy rules shouldn't be empty"), w)
		return
	}

	if policyID == "" {
		policyID = xid.New().String()
	}

	policy := server.Policy{
		ID:          policyID,
		Name:        req.Name,
		Enabled:     req.Enabled,
		Description: req.Description,
	}
	for _, r := range req.Rules {
		pr := server.PolicyRule{
			ID:            policyID, //TODO: when policy can contain multiple rules, need refactor
			Name:          r.Name,
			Destinations:  groupMinimumsToStrings(account, r.Destinations),
			Sources:       groupMinimumsToStrings(account, r.Sources),
			Bidirectional: r.Bidirectional,
		}

		pr.Enabled = r.Enabled
		if r.Description != nil {
			pr.Description = *r.Description
		}

		switch r.Action {
		case api.PolicyRuleUpdateActionAccept:
			pr.Action = server.PolicyTrafficActionAccept
		case api.PolicyRuleUpdateActionDrop:
			pr.Action = server.PolicyTrafficActionDrop
		default:
			util.WriteError(status.Errorf(status.InvalidArgument, "unknown action type"), w)
			return
		}

		switch r.Protocol {
		case api.PolicyRuleUpdateProtocolAll:
			pr.Protocol = server.PolicyRuleProtocolALL
		case api.PolicyRuleUpdateProtocolTcp:
			pr.Protocol = server.PolicyRuleProtocolTCP
		case api.PolicyRuleUpdateProtocolUdp:
			pr.Protocol = server.PolicyRuleProtocolUDP
		case api.PolicyRuleUpdateProtocolIcmp:
			pr.Protocol = server.PolicyRuleProtocolICMP
		default:
			util.WriteError(status.Errorf(status.InvalidArgument, "unknown protocol type: %v", r.Protocol), w)
			return
		}

		if r.Ports != nil && len(*r.Ports) != 0 {
			for _, v := range *r.Ports {
				if port, err := strconv.Atoi(v); err != nil || port < 1 || port > 65535 {
					util.WriteError(status.Errorf(status.InvalidArgument, "valid port value is in 1..65535 range"), w)
					return
				}
				pr.Ports = append(pr.Ports, v)
			}
		}

		// validate policy object
		switch pr.Protocol {
		case server.PolicyRuleProtocolALL, server.PolicyRuleProtocolICMP:
			if len(pr.Ports) != 0 {
				util.WriteError(status.Errorf(status.InvalidArgument, "for ALL or ICMP protocol ports is not allowed"), w)
				return
			}
			if !pr.Bidirectional {
				util.WriteError(status.Errorf(status.InvalidArgument, "for ALL or ICMP protocol type flow can be only bi-directional"), w)
				return
			}
		case server.PolicyRuleProtocolTCP, server.PolicyRuleProtocolUDP:
			if !pr.Bidirectional && len(pr.Ports) == 0 {
				util.WriteError(status.Errorf(status.InvalidArgument, "for ALL or ICMP protocol type flow can be only bi-directional"), w)
				return
			}
		}

		policy.Rules = append(policy.Rules, &pr)
	}

	if req.SourcePostureChecks != nil {
		policy.SourcePostureChecks = sourcePostureChecksToStrings(account, *req.SourcePostureChecks)
	}

	if err := h.accountManager.SavePolicy(account.Id, user.Id, &policy); err != nil {
		util.WriteError(err, w)
		return
	}

	resp := toPolicyResponse(account, &policy)
	if len(resp.Rules) == 0 {
		util.WriteError(status.Errorf(status.Internal, "no rules in the policy"), w)
		return
	}

	util.WriteJSONObject(w, resp)
}

// DeletePolicy handles policy deletion request
func (h *Policies) DeletePolicy(w http.ResponseWriter, r *http.Request) {
	claims := h.claimsExtractor.FromRequestContext(r)
	account, user, err := h.accountManager.GetAccountFromToken(claims)
	if err != nil {
		util.WriteError(err, w)
		return
	}
	aID := account.Id

	vars := mux.Vars(r)
	policyID := vars["policyId"]
	if len(policyID) == 0 {
		util.WriteError(status.Errorf(status.InvalidArgument, "invalid policy ID"), w)
		return
	}

	if err = h.accountManager.DeletePolicy(aID, policyID, user.Id); err != nil {
		util.WriteError(err, w)
		return
	}

	util.WriteJSONObject(w, emptyObject{})
}

// GetPolicy handles a group Get request identified by ID
func (h *Policies) GetPolicy(w http.ResponseWriter, r *http.Request) {
	claims := h.claimsExtractor.FromRequestContext(r)
	account, user, err := h.accountManager.GetAccountFromToken(claims)
	if err != nil {
		util.WriteError(err, w)
		return
	}

	switch r.Method {
	case http.MethodGet:
		vars := mux.Vars(r)
		policyID := vars["policyId"]
		if len(policyID) == 0 {
			util.WriteError(status.Errorf(status.InvalidArgument, "invalid policy ID"), w)
			return
		}

		policy, err := h.accountManager.GetPolicy(account.Id, policyID, user.Id)
		if err != nil {
			util.WriteError(err, w)
			return
		}

		resp := toPolicyResponse(account, policy)
		if len(resp.Rules) == 0 {
			util.WriteError(status.Errorf(status.Internal, "no rules in the policy"), w)
			return
		}

		util.WriteJSONObject(w, resp)
	default:
		util.WriteError(status.Errorf(status.NotFound, "method not found"), w)
	}
}

func toPolicyResponse(account *server.Account, policy *server.Policy) *api.Policy {
	cache := make(map[string]api.GroupMinimum)
	ap := &api.Policy{
		Id:                  &policy.ID,
		Name:                policy.Name,
		Description:         policy.Description,
		Enabled:             policy.Enabled,
		SourcePostureChecks: policy.SourcePostureChecks,
	}
	for _, r := range policy.Rules {
		rID := r.ID
		rDescription := r.Description
		rule := api.PolicyRule{
			Id:            &rID,
			Name:          r.Name,
			Enabled:       r.Enabled,
			Description:   &rDescription,
			Bidirectional: r.Bidirectional,
			Protocol:      api.PolicyRuleProtocol(r.Protocol),
			Action:        api.PolicyRuleAction(r.Action),
		}
		if len(r.Ports) != 0 {
			portsCopy := r.Ports
			rule.Ports = &portsCopy
		}
		for _, gid := range r.Sources {
			_, ok := cache[gid]
			if ok {
				continue
			}
			if group, ok := account.Groups[gid]; ok {
				minimum := api.GroupMinimum{
					Id:         group.ID,
					Name:       group.Name,
					PeersCount: len(group.Peers),
				}
				rule.Sources = append(rule.Sources, minimum)
				cache[gid] = minimum
			}
		}
		for _, gid := range r.Destinations {
			cachedMinimum, ok := cache[gid]
			if ok {
				rule.Destinations = append(rule.Destinations, cachedMinimum)
				continue
			}
			if group, ok := account.Groups[gid]; ok {
				minimum := api.GroupMinimum{
					Id:         group.ID,
					Name:       group.Name,
					PeersCount: len(group.Peers),
				}
				rule.Destinations = append(rule.Destinations, minimum)
				cache[gid] = minimum
			}
		}
		ap.Rules = append(ap.Rules, rule)
	}
	return ap
}

func groupMinimumsToStrings(account *server.Account, gm []string) []string {
	result := make([]string, 0, len(gm))
	for _, g := range gm {
		if _, ok := account.Groups[g]; !ok {
			continue
		}
		result = append(result, g)
	}
	return result
}

func sourcePostureChecksToStrings(account *server.Account, postureChecksIds []string) []string {
	result := make([]string, 0, len(postureChecksIds))
	for _, id := range postureChecksIds {
		for _, postureCheck := range account.PostureChecks {
			if id == postureCheck.ID {
				result = append(result, id)
				continue
			}
		}

	}
	return result
}
